package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/log"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/remeh/sizedwaitgroup"
	"github.com/valyala/fasttemplate"
	"golang.org/x/net/publicsuffix"
)

const (
	author  = "redteam"
	version = "1.0.0"
)

var banner = fmt.Sprintf(`
██╗  ██╗ ██████╗ ███╗   ██╗
██║ ██╔╝██╔═══██╗████╗  ██║
█████╔╝ ██║   ██║██╔██╗ ██║
██╔═██╗ ██║   ██║██║╚██╗██║
██║  ██╗╚██████╔╝██║ ╚████║
╚═╝  ╚═╝ ╚═════╝ ╚═╝  ╚═══╝
                
                -->
                   __
                  / /_ _   _____  _____
                 /__  \ | / / _ \/ ___/
                / /_/ / |/ /  __/ /
                \____/|___/\___/_/

   Kon - Recon Orchestrator v%s
        Kon -i [URL|file.txt] [OPTIONS]
`, version)

var clog *log.Logger

// =============== OPTIONS ===============

type Options struct {
	Input        string
	Output       string
	Combine      bool
	Replace      string
	SkipPaths    []string
	Extensions   string
	Concurrency  int
	Depth        int
	SameHost     bool
	SameRoot     bool
	Timeout      int
	Wait         int
	Silent       bool
	Template     string
	File         *os.File
	Scanner      *bufio.Scanner
}

// =============== INIT & VALIDATION ===============

func init() {
	clog = log.NewWithOptions(os.Stderr, log.Options{
		ReportTimestamp: true,
		TimeFormat:      time.Kitchen,
	})
}

func parseFlags() *Options {
	opt := &Options{}

	flag.StringVar(&opt.Input, "i", "", "Input file or URL (or stdin)")
	flag.StringVar(&opt.Output, "o", "", "Output file")
	flag.BoolVar(&opt.Combine, "combine", false, "Combine query parameters by base URL")
	flag.StringVar(&opt.Replace, "r", "", "Replace all parameter values with given string")
	flag.Var((*skipPaths)(&opt.SkipPaths), "skip-path", "Skip paths matching regex (can be used multiple times)")
	flag.StringVar(&opt.Extensions, "e", "", "Filter by extensions (comma-separated, e.g., js,php)")
	flag.IntVar(&opt.Concurrency, "c", 30, "Concurrency level")
	flag.IntVar(&opt.Depth, "d", 2, "Crawl depth")
	flag.BoolVar(&opt.SameHost, "same-host", false, "Restrict to same host")
	flag.BoolVar(&opt.SameRoot, "same-root", true, "Restrict to same eTLD+1 (default: true)")
	flag.IntVar(&opt.Timeout, "t", 45, "Timeout in seconds")
	flag.IntVar(&opt.Wait, "w", 2, "Wait time after page load (seconds)")
	flag.StringVar(&opt.Template, "T", "{{raw_url}}", "Output template")
	flag.BoolVar(&opt.Silent, "s", false, "Silent mode")

	flag.Usage = func() {
		fmt.Fprint(os.Stderr, banner)
		fmt.Fprintf(os.Stderr, "Usage: %s -i [URL|file.txt] [OPTIONS]\n", os.Args[0])
		flag.PrintDefaults()
	}

	flag.Parse()

	if !opt.Silent {
		fmt.Fprint(os.Stderr, banner)
	}

	// Input handling
	if isStdin() {
		opt.Scanner = bufio.NewScanner(os.Stdin)
	} else if opt.Input != "" {
		if strings.HasPrefix(opt.Input, "http") {
			opt.Scanner = bufio.NewScanner(strings.NewReader(opt.Input))
		} else {
			f, err := os.Open(opt.Input)
			if err != nil {
				clog.Fatal("failed to open input", "err", err)
			}
			opt.Scanner = bufio.NewScanner(f)
		}
	} else {
		clog.Fatal("no input provided (-i required or pipe via stdin)")
	}

	if opt.Output != "" {
		var err error
		opt.File, err = os.OpenFile(opt.Output, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			clog.Fatal("failed to open output", "err", err)
		}
	}

	return opt
}

// =============== SKIP PATHS FLAG ===============

type skipPaths []string

func (s *skipPaths) String() string { return "" }
func (s *skipPaths) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// =============== MAIN RUNNER ===============

type Runner struct {
	opt    *Options
	swg    sizedwaitgroup.SizedWaitGroup
	seen   map[string]bool
	writer io.Writer
}

func NewRunner(opt *Options) *Runner {
	w := io.Writer(os.Stdout)
	if opt.File != nil {
		w = io.MultiWriter(os.Stdout, opt.File)
	}

	return &Runner{
		opt:    opt,
		swg:    sizedwaitgroup.New(opt.Concurrency),
		seen:   make(map[string]bool),
		writer: w,
	}
}

func (r *Runner) Run() {
	jobs := make(chan string)

	// Worker pool
	for i := 0; i < r.opt.Concurrency; i++ {
		r.swg.Add()
		go func() {
			defer r.swg.Done()
			for u := range jobs {
				r.processSeed(u)
			}
		}()
	}

	// Feed seeds
	for r.opt.Scanner.Scan() {
		u := strings.TrimSpace(r.opt.Scanner.Text())
		if u == "" || !isValidURL(u) {
			continue
		}
		jobs <- u
	}
	close(jobs)
	r.swg.Wait()

	if r.opt.File != nil {
		r.opt.File.Close()
	}
}

func (r *Runner) processSeed(seed string) {
	// Step 1: Normalize & dedupe base URLs with query params
	baseMap := r.normalizeURLs([]string{seed})

	// Step 2: Crawl each base URL deeply
	for base, query := range baseMap {
		fullURL := base + qMark(query)
		r.crawlAndExtract(fullURL, 1)
	}
}

func (r *Runner) normalizeURLs(urls []string) map[string]string {
	m := make(map[string]string)
	for _, raw := range urls {
		u, err := url.ParseRequestURI(raw)
		if err != nil {
			continue
		}
		if matchPath(r.opt.SkipPaths, u) {
			continue
		}

		key := fmt.Sprintf("%s://%s%s", u.Scheme, u.Host, u.Path)
		if _, exists := m[key]; exists {
			if r.opt.Combine {
				combined := remDup(m[key] + "&" + u.RawQuery)
				m[key] = combined
			}
		} else {
			m[key] = u.RawQuery
		}
	}

	// Apply replace if needed
	if r.opt.Replace != "" {
		for k, v := range m {
			q, _ := url.ParseQuery(v)
			for p := range q {
				q.Set(p, r.opt.Replace)
			}
			m[k] = q.Encode()
		}
	}

	return m
}

func (r *Runner) crawlAndExtract(startURL string, depth int) {
	if depth > r.opt.Depth || r.seen[startURL] {
		return
	}
	r.seen[startURL] = true

	cfg := NewCrawlerConfig(r.opt, startURL)
	extracted, err := cfg.Crawl(startURL)
	if err != nil && !r.opt.Silent {
		clog.Warn("crawl failed", "url", startURL, "err", err)
		return
	}

	// Output each extracted URL
	for _, u := range extracted {
		if r.seen[u] {
			continue
		}
		r.seen[u] = true

		// Extension filter
		if r.opt.Extensions != "" && !r.isOnExt(u) {
			continue
		}

		// Template formatting
		out := r.formatOutput(u)

		fmt.Fprintln(r.writer, out)

		// Recursive crawl if depth allows
		if depth < r.opt.Depth {
			r.swg.Add()
			go func(url string, d int) {
				defer r.swg.Done()
				r.crawlAndExtract(url, d)
			}(u, depth+1)
		}
	}
}

func (r *Runner) isOnExt(URL string) bool {
	ext := strings.TrimLeft(filepath.Ext(URL), ".")
	for _, e := range strings.Split(r.opt.Extensions, ",") {
		if ext == strings.TrimSpace(e) {
			return true
		}
	}
	return false
}

func (r *Runner) formatOutput(rawURL string) string {
	if r.opt.Template == "{{raw_url}}" {
		return rawURL
	}

	tmpl := fasttemplate.New(r.opt.Template, "{{", "}}")
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}

	pw := ""
	if u.User != nil {
		if p, ok := u.User.Password(); ok {
			pw = p
		}
	}

	data := map[string]interface{}{
		"raw_url":        rawURL,
		"scheme":         u.Scheme,
		"user":           u.User.String(),
		"username":       u.User.Username(),
		"password":       pw,
		"host":           u.Host,
		"hostname":       u.Hostname(),
		"port":           u.Port(),
		"path":           u.Path,
		"raw_path":       u.RawPath,
		"escaped_path":   url.PathEscape(u.Path),
		"raw_query":      u.RawQuery,
		"fragment":       u.Fragment,
		"raw_fragment":   u.RawFragment,
	}

	return tmpl.ExecuteString(data)
}

// =============== CRAWLER CONFIG ===============

type CrawlerConfig struct {
	Options *Options
	Scope   struct {
		hostname string
		root     string
	}
	Template *fasttemplate.Template
	Ctx      context.Context
	Cancel   context.CancelFunc
	Logger   *log.Logger
}

var execAllocOpts = append(
	chromedp.DefaultExecAllocatorOptions[:],
	chromedp.DisableGPU,
	chromedp.IgnoreCertErrors,
	chromedp.NoDefaultBrowserCheck,
	chromedp.NoFirstRun,
)

func NewCrawlerConfig(opt *Options, seedURL string) *CrawlerConfig {
	cfg := &CrawlerConfig{Options: opt, Logger: clog}

	// Set scope
	u, _ := url.Parse(seedURL)
	cfg.Scope.hostname = u.Hostname()
	cfg.Scope.root, _ = publicsuffix.EffectiveTLDPlusOne(cfg.Scope.hostname)

	// Context
	allocCtx, _ := chromedp.NewExecAllocator(context.Background(), execAllocOpts...)
	ctx, cancel := chromedp.NewContext(allocCtx)
	ctx, cancel = context.WithTimeout(ctx, time.Duration(opt.Timeout)*time.Second)
	cfg.Ctx = ctx
	cfg.Cancel = cancel

	if opt.Template != "{{raw_url}}" {
		cfg.Template = fasttemplate.New(opt.Template, "{{", "}}")
	}

	return cfg
}

func (cfg *CrawlerConfig) Crawl(URL string) ([]string, error) {
	var res, reqs []string

	if !isValidURL(URL) {
		return nil, errors.New("invalid URL")
	}

	ctx, cancel := chromedp.NewContext(cfg.Ctx)
	defer cancel()

	chromedp.ListenTarget(ctx, func(ev interface{}) {
		if ev, ok := ev.(*network.EventRequestWillBeSent); ok {
			reqURL := ev.Request.URL
			if reqURL != URL && isValidURL(reqURL) && !contains(reqs, reqURL) {
				reqs = append(reqs, reqURL)
			}
		}
	})

	script := `(() => {
		let urls = [];
		const tags = ['a', 'link', 'script', 'img', 'iframe', 'form'];
		tags.forEach(tag => {
			document.querySelectorAll(tag).forEach(el => {
				let u = el.href || el.src || el.action || '';
				if (u && u.startsWith('http')) urls.push(u);
			});
		});
		return [...new Set(urls)];
	})()`

	err := chromedp.Run(ctx,
		chromedp.Navigate(URL),
		chromedp.Sleep(time.Duration(cfg.Options.Wait)*time.Second),
		chromedp.Evaluate(script, &res),
	)
	if err != nil {
		return nil, err
	}

	all := mergeSlices(res, reqs)

	// Filtering
	filtered := all[:0]
	for _, u := range all {
		parsed, err := url.Parse(u)
		if err != nil {
			continue
		}

		include := true
		if cfg.Options.SameRoot {
			root, err := publicsuffix.EffectiveTLDPlusOne(parsed.Hostname())
			if err != nil || root != cfg.Scope.root {
				include = false
			}
		} else if cfg.Options.SameHost {
			if parsed.Hostname() != cfg.Scope.hostname {
				include = false
			}
		}

		if include {
			filtered = append(filtered, u)
		}
	}

	return filtered, nil
}

// =============== UTILS ===============

func isStdin() bool {
	stat, err := os.Stdin.Stat()
	return err == nil && (stat.Mode()&os.ModeNamedPipe) != 0
}

func isValidURL(s string) bool {
	_, err := url.ParseRequestURI(s)
	return err == nil
}

func matchPath(patterns []string, u *url.URL) bool {
	for _, p := range patterns {
		if ok, _ := regexp.MatchString(p, u.Path); ok {
			return true
		}
	}
	return false
}

func qMark(q string) string {
	if q == "" {
		return ""
	}
	return "?" + q
}

func remDup(q string) string {
	vals, _ := url.ParseQuery(q)
	result := url.Values{}
	for k, v := range vals {
		if len(v) > 0 {
			result.Set(k, v[0])
		}
	}
	return result.Encode()
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func mergeSlices(a, b []string) []string {
	m := make(map[string]bool)
	var result []string
	for _, item := range a {
		if !m[item] {
			m[item] = true
			result = append(result, item)
		}
	}
	for _, item := range b {
		if !m[item] {
			m[item] = true
			result = append(result, item)
		}
	}
	return result
}

// =============== MAIN ===============

func main() {
	opt := parseFlags()
	runner := NewRunner(opt)
	runner.Run()
}
