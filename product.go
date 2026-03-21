package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"sort"
	"strings"
	"time"
)

// ─── Auto-detect cheapest available product on a Shopify store ───────────────

func autoDetectProduct(client *http.Client, shopURL string, fp Fingerprint) *Product {
	fmt.Println("[0/5] Auto-detecting cheapest product...")

	// Strategy 1: products.json (fastest)
	if p := tryProductsJSON(client, shopURL, fp, "/products.json?limit=250"); p != nil {
		fmt.Printf("  ✅ Cheapest product found via products.json: %s $%s\n", p.Title, p.PriceStr)
		return p
	}

	time.Sleep(jitter(50, 150))

	// Strategy 2: collections/all
	if p := tryProductsJSON(client, shopURL, fp, "/collections/all/products.json?limit=250"); p != nil {
		fmt.Printf("  ✅ Cheapest product found via collections/all: %s $%s\n", p.Title, p.PriceStr)
		return p
	}

	if cfg.FastMode {
		fmt.Println("  [FAST] Skipping slow sitemap/search in FAST_MODE")
		return nil
	}

	// Strategy 3: predictive search (slower fallback)
	if p := tryPredictiveSearch(client, shopURL, fp); p != nil {
		fmt.Printf("  ✅ Cheapest product found via search: %s $%s\n", p.Title, p.PriceStr)
		return p
	}

	fmt.Println("  ❌ Could not auto-detect any products")
	return nil
}

func tryProductsJSON(client *http.Client, shopURL string, fp Fingerprint, path string) *Product {
	time.Sleep(jitter(50, 150))

	req, err := http.NewRequest("GET", shopURL+path, nil)
	if err != nil {
		return nil
	}
	setBrowseHeaders(req, fp, shopURL)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil
	}
	decompressBody(resp)
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}

	var data struct {
		Products []jsonProduct `json:"products"`
	}
	if err := json.Unmarshal(bodyBytes, &data); err != nil {
		return nil
	}

	return cheapestProduct(data.Products)
}

type jsonProduct struct {
	ID       json.Number `json:"id"`
	Title    string      `json:"title"`
	Variants []struct {
		ID              json.Number `json:"id"`
		Price           string      `json:"price"`
		Available       *bool       `json:"available"`
		InventoryQty    *int        `json:"inventory_quantity"`
		InventoryPolicy string      `json:"inventory_policy"`
	} `json:"variants"`
}

func cheapestProduct(products []jsonProduct) *Product {
	type candidate struct {
		pid, vid, priceStr, title string
		price                     float64
	}
	var candidates []candidate

	for _, p := range products {
		for _, v := range p.Variants {
			price := parsePrice(v.Price)
			if price <= 0 {
				continue
			}
			if cfg.MaxPrice > 0 && price > cfg.MaxPrice {
				continue
			}

			available := false
			if v.Available != nil {
				available = *v.Available
			} else if v.InventoryQty != nil {
				available = *v.InventoryQty > 0
			}
			if !available && strings.EqualFold(v.InventoryPolicy, "continue") {
				available = true
			}
			if !available {
				continue
			}

			candidates = append(candidates, candidate{
				pid:      p.ID.String(),
				vid:      v.ID.String(),
				priceStr: v.Price,
				title:    p.Title,
				price:    price,
			})
		}
	}

	if len(candidates) == 0 {
		return nil
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].price < candidates[j].price
	})

	c := candidates[0]
	return &Product{
		ID:        c.pid,
		VariantID: c.vid,
		Price:     c.price,
		PriceStr:  c.priceStr,
		Title:     c.title,
	}
}

func tryPredictiveSearch(client *http.Client, shopURL string, fp Fingerprint) *Product {
	searchURL := shopURL + "/search/suggest.json?q=a&resources[type]=product&resources[limit]=10"
	req, err := http.NewRequest("GET", searchURL, nil)
	if err != nil {
		return nil
	}
	setBrowseHeaders(req, fp, shopURL)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		if resp != nil {
			resp.Body.Close()
		}
		return nil
	}
	decompressBody(resp)
	defer resp.Body.Close()

	var data struct {
		Resources struct {
			Results struct {
				Products []struct {
					Handle string `json:"handle"`
				} `json:"products"`
			} `json:"results"`
		} `json:"resources"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil
	}

	var best *Product
	for _, p := range data.Resources.Results.Products {
		if p.Handle == "" {
			continue
		}
		pURL := fmt.Sprintf("%s/products/%s.js", shopURL, p.Handle)
		req2, err := http.NewRequest("GET", pURL, nil)
		if err != nil {
			continue
		}
		setBrowseHeaders(req2, fp, shopURL)
		req2.Header.Set("Accept", "application/json")

		resp2, err := client.Do(req2)
		if err != nil || resp2.StatusCode != 200 {
			if resp2 != nil {
				resp2.Body.Close()
			}
			continue
		}
		decompressBody(resp2)

		var pdata jsonProduct
		json.NewDecoder(resp2.Body).Decode(&pdata)
		resp2.Body.Close()

		candidate := cheapestProduct([]jsonProduct{pdata})
		if candidate != nil && (cfg.MaxPrice <= 0 || candidate.Price <= cfg.MaxPrice) && (best == nil || candidate.Price < best.Price) {
			best = candidate
		}
	}
	return best
}

// ─── Helper: set browser-like headers for GET requests ───────────────────────

func setBrowseHeaders(req *http.Request, fp Fingerprint, shopURL string) {
	req.Header.Set("User-Agent", fp.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	req.Header.Set("Sec-CH-UA", fp.SecCHUA)
	req.Header.Set("Sec-CH-UA-Mobile", fp.SecCHUAMobile)
	req.Header.Set("Sec-CH-UA-Platform", fp.SecCHUAPlatform)
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "none")
	req.Header.Set("Sec-Fetch-User", "?1")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	if shopURL != "" {
		req.Header.Set("Referer", shopURL+"/")
	}
}

func jitter(minMS, maxMS int) time.Duration {
	ms := minMS + rand.IntN(maxMS-minMS+1)
	return time.Duration(ms) * time.Millisecond
}
