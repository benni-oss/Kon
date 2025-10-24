# Kon — Knowledge Orchestrator for Recon

🔍 A powerful, browser-driven recon tool for pentesters that combines URL normalization, parameter fuzzing prep, and deep crawling to map hidden attack surfaces.


## Fitur

 **Headless Browser Crawling**: Uses Chrome/Chromium via chromedp to render JavaScript and extract dynamically loaded URLs (e.g., from SPAs, AJAX calls, iframes).
- **Smart URL Normalization & Deduplication**: Groups URLs by base path and optionally merges query parameters.
- **Parameter Fuzzing Preparation**: Combine all parameters from similar endpoints into one URL.
- **Domain Relationship Mapping**: Automatically discovers related subdomains and external domains via resource loading (scripts, images, API calls)
- **Extension-Based Filtering**: Extract only URLs with specific extensions (e.g., .js, .json, .php, .xml) to focus on high-value assets.
- **Customizable Output Templates**

## Prerequisites

### External Tools
```bash
# For Kali Linux / Debian / Ubuntu
sudo apt install -y chromium-driver

# Instal Kon
go install github.com/benni-oss/Kon@latest
```

## Konfigurasi

### Environment Variables

Buat file `.env` atau set environment variables:

```bash
echo 'export PATH="$PATH:$(go env GOPATH)/bin"' >> ~/.bashrc && source ~/.bashrc
```

## Fast Using

### Crawl target & ekstrak all URL
```bash
Kon -i https://target.com
```
### Fokus on sensitive file (JS, JSON, dll)
```bash
Kon -i https://app.target.com -e js,json,xml -d 2
```
### Prepare for fuzzing parameter
```bash
another_tools target.com | Kon -combine -r FUZZ -o fuzz.txt
```

### WHY Kon ?
1. Dynamic endpoint detection that only occurs after JavaScript is executed
2. Automatically discover relationships between subdomains via resource sharing
3. Prepare high quality input for ffuf, dalfox, or Burp Intruder
4. Minimal false positives thanks to eTLD+1 based filtering

## License

Distributed in MIT License .

# Only For Ethical hacking etik Using don't do use this tools in illegal purpose

