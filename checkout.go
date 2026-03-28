package main

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/andybalholm/brotli"
)

// ─── Decompression helper ────────────────────────────────────────────────────

func decompressBody(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	switch strings.ToLower(resp.Header.Get("Content-Encoding")) {
	case "gzip":
		gr, err := gzip.NewReader(resp.Body)
		if err == nil {
			resp.Body = gr
		}
	case "br":
		resp.Body = io.NopCloser(brotli.NewReader(resp.Body))
	case "deflate":
		resp.Body = flate.NewReader(resp.Body)
	}
}

// ─── Test / Bogus Gateway Detection ─────────────────────────────────────────

var testModePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)bogus\s*gateway`),
	regexp.MustCompile(`(?i)test\s*mode`),
	regexp.MustCompile(`(?i)payments?.*\(test\s*mode\)`),
	regexp.MustCompile(`(?i)sandbox`),
	regexp.MustCompile(`(?i)"paymentGateway"\s*:\s*"bogus"`),
	regexp.MustCompile(`(?i)"testMode"\s*:\s*true`),
	regexp.MustCompile(`(?i)"is_test"\s*:\s*true`),
	regexp.MustCompile(`(?i)data-test-mode\s*=\s*["']true`),
	regexp.MustCompile(`(?i)shopify.*payments?.*test`),
	regexp.MustCompile(`(?i)"provider"\s*:\s*"bogus"`),
}

func detectTestMode(body string) bool {
	if len(body) == 0 {
		return false
	}
	sample := body
	if len(sample) > 50000 {
		sample = sample[:50000]
	}
	for _, pat := range testModePatterns {
		if pat.MatchString(sample) {
			return true
		}
	}
	return false
}

// ─── HTML Extraction Helpers (V2 — from newworking_main.go) ─────────────────

func extractStableID(checkoutHTML string) string {
	unescaped := html.UnescapeString(checkoutHTML)
	re := regexp.MustCompile(`"stableId"\s*:\s*"([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})"`)
	m := re.FindStringSubmatch(unescaped)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func extractCommitSha(checkoutHTML string) string {
	unescaped := html.UnescapeString(checkoutHTML)
	re := regexp.MustCompile(`"commitSha"\s*:\s*"([a-f0-9]{40})"`)
	m := re.FindStringSubmatch(unescaped)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func extractSourceToken(checkoutHTML string) string {
	re := regexp.MustCompile(`<meta\s+name="serialized-sourceToken"\s+content="([^"]*)"`)
	m := re.FindStringSubmatch(checkoutHTML)
	if len(m) < 2 {
		return ""
	}
	val := html.UnescapeString(m[1])
	return strings.Trim(val, `"`)
}

func extractIdentificationSignature(checkoutHTML string) string {
	unescaped := html.UnescapeString(checkoutHTML)
	re := regexp.MustCompile(`checkoutCardsinkCallerIdentificationSignature":"([^"]+)"`)
	m := re.FindStringSubmatch(unescaped)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func extractPrivateAccessTokenID(checkoutHTML string) string {
	unescaped := html.UnescapeString(checkoutHTML)
	re := regexp.MustCompile(`"checkoutSessionIdentifier"\s*:\s*"([a-f0-9]+)"`)
	m := re.FindStringSubmatch(unescaped)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func extractActionsJSURL(checkoutHTML, shopURL string) string {
	re := regexp.MustCompile(`(/cdn/shopifycloud/checkout-web/assets/c1/actions[A-Za-z0-9_-]*\.[A-Za-z0-9_-]+\.js)`)
	m := re.FindStringSubmatch(checkoutHTML)
	if len(m) < 2 {
		return ""
	}
	return shopURL + m[1]
}

func extractProcessingJSURL(checkoutHTML, shopURL string) string {
	re := regexp.MustCompile(`(/cdn/shopifycloud/checkout-web/assets/c1/useHasOrdersFromMultipleShops[A-Za-z0-9_.-]*\.js)`)
	m := re.FindStringSubmatch(checkoutHTML)
	if len(m) < 2 {
		return ""
	}
	return shopURL + m[1]
}

func extractProposalID(jsBody string) string {
	re := regexp.MustCompile(`id:\s*"([a-f0-9]{64})"\s*,\s*type:\s*"query"\s*,\s*name:\s*"Proposal"`)
	m := re.FindStringSubmatch(jsBody)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func extractSubmitForCompletionID(jsBody string) string {
	re := regexp.MustCompile(`id:\s*"([a-f0-9]{64})"\s*,\s*type:\s*"mutation"\s*,\s*name:\s*"SubmitForCompletion"`)
	m := re.FindStringSubmatch(jsBody)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func extractPollForReceiptID(jsBody string) string {
	re := regexp.MustCompile(`id:\s*"([a-f0-9]{64})"\s*,\s*type:\s*"query"\s*,\s*name:\s*"PollForReceipt"`)
	m := re.FindStringSubmatch(jsBody)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// ─── Proposal Response Extraction Helpers ────────────────────────────────────

func extractQueueTokenStr(body string) string {
	re := regexp.MustCompile(`"queueToken"\s*:\s*"([^"]+)"`)
	m := re.FindStringSubmatch(body)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func extractDeliveryHandleStr(body string) string {
	re := regexp.MustCompile(`"handle"\s*:\s*"([^"]+)"\s*,\s*"phoneRequired"`)
	m := re.FindStringSubmatch(body)
	if len(m) >= 2 {
		return m[1]
	}
	re2 := regexp.MustCompile(`"handle"\s*:\s*"([^"]+?)"\s*,\s*"[^"]*"\s*:\s*(?:true|false)\s*,\s*"amount"`)
	m = re2.FindStringSubmatch(body)
	if len(m) >= 2 {
		return m[1]
	}
	return ""
}

func extractSignedHandlesStr(body string) []string {
	re := regexp.MustCompile(`"signedHandle"\s*:\s*"([^"]+)"`)
	matches := re.FindAllStringSubmatch(body, -1)
	var handles []string
	for _, m := range matches {
		if len(m) >= 2 {
			handles = append(handles, m[1])
		}
	}
	return handles
}

func extractShippingAmountStr(body string) string {
	re := regexp.MustCompile(`"deliveryStrategyBreakdown"\s*:\s*\[\s*\{\s*"amount"\s*:\s*\{\s*"value"\s*:\s*\{\s*"amount"\s*:\s*"([^"]+)"`)
	m := re.FindStringSubmatch(body)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func extractCheckoutTotalStr(body string) string {
	re := regexp.MustCompile(`"checkoutTotal"\s*:\s*\{[^}]*"value"\s*:\s*\{\s*"amount"\s*:\s*"([^"]+)"`)
	m := re.FindStringSubmatch(body)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func extractSellerTotalStr(body string) string {
	re := regexp.MustCompile(`"total"\s*:\s*\{\s*"value"\s*:\s*\{\s*"amount"\s*:\s*"([^"]+)"`)
	m := re.FindStringSubmatch(body)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func extractSellerCurrencyStr(body string) string {
	re := regexp.MustCompile(`"supportedCurrencies"\s*:\s*\["([^"]+)"`)
	m := re.FindStringSubmatch(body)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func extractSellerCountryStr(body string) string {
	re := regexp.MustCompile(`"supportedCountries"\s*:\s*\["([^"]+)"`)
	m := re.FindStringSubmatch(body)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func extractSellerMerchandisePriceStr(body string) string {
	re := regexp.MustCompile(`"ContextualizedProductVariantMerchandise".*?"totalAmount"\s*:\s*\{\s*"value"\s*:\s*\{\s*"amount"\s*:\s*"([^"]+)"`)
	m := re.FindStringSubmatch(body)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// ─── Token / ID Generation ──────────────────────────────────────────────────

func generateAttemptToken(checkoutToken string) string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 10)
	for i := range b {
		b[i] = chars[rand.IntN(len(chars))]
	}
	return checkoutToken + "-" + string(b)
}

func generatePageID() string {
	b := make([]byte, 16)
	for i := range b {
		b[i] = byte(rand.IntN(256))
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ─── GraphQL Headers Builder (V2 — matches newworking_main.go) ──────────────

func (cs *CheckoutSession) graphqlHeaders() http.Header {
	h := http.Header{}
	h.Set("Accept", "application/json")
	h.Set("Accept-Language", "en-US")
	h.Set("Content-Type", "application/json")
	h.Set("Origin", cs.ShopURL)
	h.Set("Priority", "u=1, i")
	if cs.CheckoutURL != "" {
		h.Set("Referer", cs.CheckoutURL)
	}
	h.Set("Sec-CH-UA", cs.FP.SecCHUA)
	h.Set("Sec-CH-UA-Mobile", cs.FP.SecCHUAMobile)
	h.Set("Sec-CH-UA-Platform", cs.FP.SecCHUAPlatform)
	h.Set("Sec-Fetch-Dest", "empty")
	h.Set("Sec-Fetch-Mode", "cors")
	h.Set("Sec-Fetch-Site", "same-origin")
	h.Set("User-Agent", cs.FP.UserAgent)
	h.Set("shopify-checkout-client", "checkout-web/1.0")
	h.Set("shopify-checkout-source", fmt.Sprintf(`id="%s", type="cn"`, cs.CheckoutToken))
	h.Set("x-checkout-one-session-token", cs.SessionToken)
	if cs.BuildID != "" {
		h.Set("x-checkout-web-build-id", cs.BuildID)
	}
	h.Set("x-checkout-web-deploy-stage", "production")
	h.Set("x-checkout-web-server-handling", "fast")
	h.Set("x-checkout-web-server-rendering", "yes")
	if cs.SourceToken != "" {
		h.Set("x-checkout-web-source-id", cs.SourceToken)
	}
	return h
}

// graphqlHeadersPoll is like graphqlHeaders but uses CheckoutToken for source-id (matches new file polling)
func (cs *CheckoutSession) graphqlHeadersPoll() http.Header {
	h := cs.graphqlHeaders()
	h.Set("x-checkout-web-source-id", cs.CheckoutToken)
	return h
}

// ─── Session warming ─────────────────────────────────────────────────────────
// V2: Minimal — cart add + GET /checkout establishes session.

func (cs *CheckoutSession) WarmStorefrontSession() {
	req, err := http.NewRequest("GET", cs.ShopURL, nil)
	if err == nil {
		setBrowseHeaders(req, cs.FP, cs.ShopURL)
		resp, err := cs.Client.Do(req)
		if err == nil {
			resp.Body.Close()
		}
	}
	time.Sleep(jitter(200, 500))
}

// ─── Step 1: Add to Cart + Checkout + Extract All IDs + Private Token + JS IDs ─

func (cs *CheckoutSession) Step1AddToCart() error {
	fmt.Println("[1/5] Cart + checkout + extracting IDs...")

	// ── Add to cart ──
	payload := fmt.Sprintf(`{"id":%s,"quantity":1}`, cs.VariantID)

	time.Sleep(jitter(100, 300))

	req, err := http.NewRequest("POST", cs.ShopURL+"/cart/add.js", strings.NewReader(payload))
	if err != nil {
		return fmt.Errorf("cart/add.js build: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", cs.FP.UserAgent)
	req.Header.Set("Origin", cs.ShopURL)
	req.Header.Set("Referer", cs.ShopURL+"/")

	resp, err := cs.Client.Do(req)
	if err != nil {
		return fmt.Errorf("cart/add.js: %w", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode == 429 {
		return fmt.Errorf("429 rate limited on cart/add")
	}
	fmt.Printf("  Cart add: %d\n", resp.StatusCode)

	time.Sleep(jitter(200, 500))

	// ── GET /checkout (follows redirects) ──
	checkoutReq, err := http.NewRequest("GET", cs.ShopURL+"/checkout", nil)
	if err != nil {
		return fmt.Errorf("checkout build: %w", err)
	}
	setBrowseHeaders(checkoutReq, cs.FP, cs.ShopURL)

	checkoutResp, err := cs.Client.Do(checkoutReq)
	if err != nil {
		return fmt.Errorf("checkout GET: %w", err)
	}
	decompressBody(checkoutResp)
	defer checkoutResp.Body.Close()

	bodyBytes, _ := io.ReadAll(checkoutResp.Body)
	checkoutHTML := string(bodyBytes)
	finalURL := checkoutResp.Request.URL.String()

	// ── Test/bogus gateway detection ──
	if detectTestMode(checkoutHTML) {
		return fmt.Errorf("TEST_MODE_DETECTED")
	}

	// ── Extract checkout token from URL ──
	tokenRe := regexp.MustCompile(`/checkouts/cn/([^/?]+)`)
	if m := tokenRe.FindStringSubmatch(finalURL); len(m) > 1 {
		cs.CheckoutToken = m[1]
	}
	if cs.CheckoutToken == "" {
		return fmt.Errorf("no checkout token in URL: %s", finalURL)
	}
	cs.CheckoutURL = finalURL
	fmt.Printf("  Token: %s\n", cs.CheckoutToken)

	// ── Extract session token ──
	cs.SessionToken = extractSessionToken(checkoutHTML)
	if cs.SessionToken == "" {
		return fmt.Errorf("session token not found in HTML")
	}

	// ── Extract stableId, commitSha, sourceToken, identificationSignature ──
	cs.StableID = extractStableID(checkoutHTML)
	cs.MerchandiseID = cs.StableID
	cs.BuildID = extractCommitSha(checkoutHTML)
	cs.SourceToken = extractSourceToken(checkoutHTML)
	cs.IdentificationSignature = extractIdentificationSignature(checkoutHTML)

	if cs.BuildID == "" {
		cs.BuildID = extractBuildID(checkoutHTML)
	}
	if cs.StableID == "" {
		return fmt.Errorf("stableId not found in checkout HTML")
	}
	if cs.SourceToken == "" {
		return fmt.Errorf("sourceToken not found in checkout HTML")
	}
	fmt.Printf("  StableID: %s BuildID: %s...\n", truncate(cs.StableID, 12), truncate(cs.BuildID, 12))

	// ── Fetch private access token (sets session cookies) ──
	patID := extractPrivateAccessTokenID(checkoutHTML)
	if patID != "" {
		patURL := fmt.Sprintf("%s/private_access_tokens?id=%s&checkout_type=c1",
			cs.ShopURL, url.QueryEscape(patID))
		patReq, err := http.NewRequest("GET", patURL, nil)
		if err == nil {
			patReq.Header.Set("Accept", "*/*")
			patReq.Header.Set("Accept-Language", "en-US,en;q=0.9")
			patReq.Header.Set("Referer", cs.CheckoutURL)
			patReq.Header.Set("Sec-CH-UA", cs.FP.SecCHUA)
			patReq.Header.Set("Sec-CH-UA-Mobile", cs.FP.SecCHUAMobile)
			patReq.Header.Set("Sec-CH-UA-Platform", cs.FP.SecCHUAPlatform)
			patReq.Header.Set("Sec-Fetch-Dest", "empty")
			patReq.Header.Set("Sec-Fetch-Mode", "cors")
			patReq.Header.Set("Sec-Fetch-Site", "same-origin")
			patReq.Header.Set("User-Agent", cs.FP.UserAgent)
			patResp, err := cs.Client.Do(patReq)
			if err == nil {
				io.Copy(io.Discard, patResp.Body)
				patResp.Body.Close()
			}
		}
		fmt.Println("  Private access token: done")
	}

	// ── Extract JS URLs and fetch GraphQL operation IDs ──
	actionsURL := extractActionsJSURL(checkoutHTML, cs.ShopURL)
	if actionsURL == "" {
		return fmt.Errorf("actions JS URL not found")
	}
	processingURL := extractProcessingJSURL(checkoutHTML, cs.ShopURL)

	actionsJS, err := cs.fetchJS(actionsURL)
	if err != nil {
		return fmt.Errorf("fetch actions JS: %w", err)
	}

	cs.ProposalID = extractProposalID(actionsJS)
	cs.SubmitID = extractSubmitForCompletionID(actionsJS)
	if cs.ProposalID == "" || cs.SubmitID == "" {
		return fmt.Errorf("proposalID or submitID not found in actions JS")
	}

	if processingURL != "" {
		processingJS, err := cs.fetchJS(processingURL)
		if err == nil {
			cs.PollForReceiptID = extractPollForReceiptID(processingJS)
		}
	}
	if cs.PollForReceiptID == "" {
		cs.PollForReceiptID = extractPollForReceiptID(actionsJS)
	}
	if cs.PollForReceiptID == "" {
		return fmt.Errorf("PollForReceipt ID not found in JS")
	}

	fmt.Printf("  Proposal: %s... Submit: %s... Poll: %s...\n",
		truncate(cs.ProposalID, 12), truncate(cs.SubmitID, 12), truncate(cs.PollForReceiptID, 12))

	return nil
}

// fetchJS fetches a JS file from the given URL
func (cs *CheckoutSession) fetchJS(jsURL string) (string, error) {
	req, err := http.NewRequest("GET", jsURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Origin", cs.ShopURL)
	req.Header.Set("Sec-CH-UA", cs.FP.SecCHUA)
	req.Header.Set("Sec-CH-UA-Mobile", cs.FP.SecCHUAMobile)
	req.Header.Set("Sec-CH-UA-Platform", cs.FP.SecCHUAPlatform)
	req.Header.Set("Sec-Fetch-Dest", "script")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("User-Agent", cs.FP.UserAgent)

	resp, err := cs.Client.Do(req)
	if err != nil {
		return "", err
	}
	decompressBody(resp)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("JS fetch HTTP %d", resp.StatusCode)
	}
	return string(body), nil
}

// ─── Step 2: Tokenize card via PCI with identification signature ─────────────

func (cs *CheckoutSession) Step2TokenizeCard() error {
	fmt.Println("[2/5] Tokenizing card...")

	time.Sleep(jitter(100, 300))

	parsed, _ := url.Parse(cs.ShopURL)
	scopeHost := parsed.Host

	payload, _ := json.Marshal(map[string]any{
		"credit_card": map[string]any{
			"number":             cs.Card.Number,
			"month":              cs.Card.Month,
			"year":               cs.Card.Year,
			"verification_value": cs.Card.CVV,
			"start_month":        nil,
			"start_year":         nil,
			"issue_number":       "",
			"name":               cs.Card.Name,
		},
		"payment_session_scope": scopeHost,
	})

	endpoint := "https://checkout.pci.shopifyinc.com/sessions"

	req, err := http.NewRequest("POST", endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("tokenize build: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Origin", "https://checkout.pci.shopifyinc.com")
	req.Header.Set("Referer", "https://checkout.pci.shopifyinc.com/build/a8e4a94/number-ltr.html?identifier=&locationURL=")
	req.Header.Set("Priority", "u=1, i")
	req.Header.Set("Sec-CH-UA", cs.FP.SecCHUA)
	req.Header.Set("Sec-CH-UA-Mobile", cs.FP.SecCHUAMobile)
	req.Header.Set("Sec-CH-UA-Platform", cs.FP.SecCHUAPlatform)
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Sec-Fetch-Storage-Access", "active")
	req.Header.Set("User-Agent", cs.FP.UserAgent)
	if cs.IdentificationSignature != "" {
		req.Header.Set("shopify-identification-signature", cs.IdentificationSignature)
	}

	tokenClient := newStandardClient(cs.ProxyURL, cfg.HTTPTimeoutShort)

	var resp *http.Response
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(attempt)*time.Second + jitter(500, 1500)
			fmt.Printf("  [RETRY] tokenize attempt %d/3\n", attempt+1)
			time.Sleep(backoff)
			req, _ = http.NewRequest("POST", endpoint, bytes.NewReader(payload))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json")
			req.Header.Set("Accept-Language", "en-US,en;q=0.9")
			req.Header.Set("Origin", "https://checkout.pci.shopifyinc.com")
			req.Header.Set("Referer", "https://checkout.pci.shopifyinc.com/build/a8e4a94/number-ltr.html?identifier=&locationURL=")
			req.Header.Set("Sec-CH-UA", cs.FP.SecCHUA)
			req.Header.Set("Sec-CH-UA-Mobile", cs.FP.SecCHUAMobile)
			req.Header.Set("Sec-CH-UA-Platform", cs.FP.SecCHUAPlatform)
			req.Header.Set("Sec-Fetch-Dest", "empty")
			req.Header.Set("Sec-Fetch-Mode", "cors")
			req.Header.Set("Sec-Fetch-Site", "same-origin")
			req.Header.Set("Sec-Fetch-Storage-Access", "active")
			req.Header.Set("User-Agent", cs.FP.UserAgent)
			if cs.IdentificationSignature != "" {
				req.Header.Set("shopify-identification-signature", cs.IdentificationSignature)
			}
		}
		resp, err = tokenClient.Do(req)
		if err != nil {
			return fmt.Errorf("tokenize POST: %w", err)
		}
		if resp.StatusCode == 429 {
			resp.Body.Close()
			if attempt == 2 {
				return fmt.Errorf("tokenization rate limited: 429 (after 3 attempts)")
			}
			continue
		}
		break
	}
	defer resp.Body.Close()

	if resp.StatusCode == 403 {
		return fmt.Errorf("tokenization blocked: 403 Forbidden (proxy/IP blocked)")
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("tokenization HTTP %d", resp.StatusCode)
	}

	var tokenResp struct {
		ID     string `json:"id"`
		Errors any    `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return fmt.Errorf("tokenize JSON decode: %w", err)
	}
	if tokenResp.ID == "" {
		return fmt.Errorf("no card session ID (errors: %v)", tokenResp.Errors)
	}

	cs.CardSessionID = tokenResp.ID
	fmt.Printf("  PCI session: %s\n", tokenResp.ID)
	return nil
}

// ─── Proposal Helpers ────────────────────────────────────────────────────────

// patchPayload replaces hardcoded USD/US with detected currency/country
func (cs *CheckoutSession) patchPayload(payload string) string {
	currency := cs.CurrencyCode
	if currency == "" {
		currency = "USD"
	}
	country := cs.DetectedCountry
	if country == "" {
		country = cs.Addr.Country
	}
	if country == "" {
		country = "US"
	}

	if currency != "USD" {
		payload = strings.ReplaceAll(payload, `"currencyCode": "USD"`, `"currencyCode": "`+currency+`"`)
		payload = strings.ReplaceAll(payload, `"presentmentCurrency": "USD"`, `"presentmentCurrency": "`+currency+`"`)
	}
	if country != "US" {
		payload = strings.ReplaceAll(payload, `"phoneCountryCode": "US"`, `"phoneCountryCode": "`+country+`"`)
	}
	return payload
}

// sendProposalRaw sends a raw proposal payload and returns body
func (cs *CheckoutSession) sendProposalRaw(payload string) (string, error) {
	gqlURL := cs.ShopURL + "/checkouts/internal/graphql/persisted?operationName=Proposal"

	req, err := http.NewRequest("POST", gqlURL, strings.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("proposal build: %w", err)
	}
	req.Header = cs.graphqlHeaders()

	var body []byte
	for attempt := range 3 {
		if attempt > 0 {
			wait := 1.5 + float64(attempt)*1.5 + rand.Float64()
			time.Sleep(time.Duration(wait * float64(time.Second)))
			req, _ = http.NewRequest("POST", gqlURL, strings.NewReader(payload))
			req.Header = cs.graphqlHeaders()
		}
		resp, err := cs.Client.Do(req)
		if err != nil {
			if attempt < 2 {
				continue
			}
			return "", fmt.Errorf("proposal POST: %w", err)
		}
		decompressBody(resp)
		body, _ = io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == 429 {
			if attempt < 2 {
				continue
			}
			return "", fmt.Errorf("proposal rate limited: 429")
		}
		if resp.StatusCode != 200 {
			return "", fmt.Errorf("proposal HTTP %d", resp.StatusCode)
		}
		break
	}

	return string(body), nil
}

// ─── Step 3: Five Proposal Rounds ────────────────────────────────────────────

func (cs *CheckoutSession) Step3Proposal() error {
	fmt.Println("[3/5] Proposals (5 rounds)...")

	email := cs.Addr.Email

	// ── Round 1: Initial proposal (empty address, no email, no queueToken) ──
	fmt.Println("  [3.1] Initial proposal...")
	payload1 := cs.buildProposal1()
	body1, err := cs.sendProposalRaw(payload1)
	if err != nil {
		return fmt.Errorf("proposal round 1: %w", err)
	}

	if cur := extractSellerCurrencyStr(body1); cur != "" {
		cs.CurrencyCode = cur
	}
	if ctr := extractSellerCountryStr(body1); ctr != "" {
		cs.DetectedCountry = ctr
	}
	if sellerPrice := extractSellerMerchandisePriceStr(body1); sellerPrice != "" {
		fmt.Printf("  Seller price: %s\n", sellerPrice)
	}

	qt1 := extractQueueTokenStr(body1)
	if qt1 == "" {
		return fmt.Errorf("queueToken not found in proposal round 1")
	}

	// ── Round 2: Proposal with email ──
	fmt.Println("  [3.2] Email proposal...")
	payload2 := cs.buildProposal2(qt1, email)
	body2, err := cs.sendProposalRaw(payload2)
	if err != nil {
		return fmt.Errorf("proposal round 2: %w", err)
	}
	qt2 := extractQueueTokenStr(body2)
	if qt2 == "" {
		return fmt.Errorf("queueToken not found in proposal round 2")
	}

	// ── Round 3: Proposal with address ──
	fmt.Println("  [3.3] Address proposal...")
	payload3 := cs.buildProposal3(qt2, email)
	body3, err := cs.sendProposalRaw(payload3)
	if err != nil {
		return fmt.Errorf("proposal round 3: %w", err)
	}
	qt3 := extractQueueTokenStr(body3)
	if qt3 == "" {
		return fmt.Errorf("queueToken not found in proposal round 3")
	}

	// ── Round 4: Delivery poll (same as round 3 — let delivery settle) ──
	fmt.Println("  [3.4] Delivery proposal...")
	time.Sleep(500 * time.Millisecond)
	payload4 := cs.buildProposal3(qt3, email)
	body4, err := cs.sendProposalRaw(payload4)
	if err != nil {
		return fmt.Errorf("proposal round 4: %w", err)
	}
	qt4 := extractQueueTokenStr(body4)
	if qt4 == "" {
		return fmt.Errorf("queueToken not found in proposal round 4")
	}

	// ── Round 5: Final proposal (extract delivery data) ──
	fmt.Println("  [3.5] Final proposal...")
	time.Sleep(500 * time.Millisecond)
	payload5 := cs.buildProposal3(qt4, email)
	body5, err := cs.sendProposalRaw(payload5)
	if err != nil {
		return fmt.Errorf("proposal round 5: %w", err)
	}

	cs.QueueToken = extractQueueTokenStr(body5)
	if cs.QueueToken == "" {
		return fmt.Errorf("queueToken not found in final proposal")
	}

	cs.ShippingHandle = extractDeliveryHandleStr(body5)
	if cs.ShippingHandle == "" {
		var resp map[string]any
		if json.Unmarshal([]byte(body5), &resp) == nil {
			result := navigateMap(resp, "data", "session", "negotiate", "result")
			sp := getMap(result, "sellerProposal")
			dt := getMap(sp, "delivery")
			if getString(dt, "__typename") == "FilledDeliveryTerms" {
				lines := getSlice(dt, "deliveryLines")
				if len(lines) > 0 {
					firstLine, _ := lines[0].(map[string]any)
					strategies := getSlice(firstLine, "availableDeliveryStrategies")
					if len(strategies) > 0 {
						first, _ := strategies[0].(map[string]any)
						cs.ShippingHandle = getString(first, "handle")
					}
				}
			}
		}
	}
	if cs.ShippingHandle == "" {
		return fmt.Errorf("no shipping handle obtained")
	}

	cs.ShippingAmount = extractShippingAmountStr(body5)
	if cs.ShippingAmount == "" {
		cs.ShippingAmount = "0.00"
	}

	totalAmount := extractCheckoutTotalStr(body5)
	if totalAmount == "" {
		totalAmount = extractSellerTotalStr(body5)
	}
	if totalAmount != "" {
		cs.ActualTotal = totalAmount
	}

	signedHandles := extractSignedHandlesStr(body5)
	cs.DeliveryExps = nil
	for _, sh := range signedHandles {
		cs.DeliveryExps = append(cs.DeliveryExps, map[string]string{"signedHandle": sh})
	}

	fmt.Printf("  Handle: %s Shipping: %s Total: %s\n",
		truncate(cs.ShippingHandle, 30), cs.ShippingAmount, cs.ActualTotal)

	return nil
}

// buildProposal1 builds the initial proposal payload (no email, no queueToken, empty address)
func (cs *CheckoutSession) buildProposal1() string {
	p := fmt.Sprintf(`{
  "variables": {
    "sessionInput": {"sessionToken": %s},
    "queueToken": null,
    "discounts": {"lines": [], "acceptUnexpectedDiscounts": true},
    "delivery": {
      "deliveryLines": [{
        "destination": {
          "partialStreetAddress": {
            "address1": "", "city": "", "countryCode": "US",
            "lastName": "", "phone": "", "oneTimeUse": false
          }
        },
        "selectedDeliveryStrategy": {
          "deliveryStrategyMatchingConditions": {
            "estimatedTimeInTransit": {"any": true},
            "shipments": {"any": true}
          },
          "options": {}
        },
        "targetMerchandiseLines": {"any": true},
        "deliveryMethodTypes": ["SHIPPING"],
        "expectedTotalPrice": {"any": true},
        "destinationChanged": true
      }],
      "noDeliveryRequired": [],
      "useProgressiveRates": false,
      "prefetchShippingRatesStrategy": null,
      "supportsSplitShipping": true
    },
    "deliveryExpectations": {"deliveryExpectationLines": []},
    "merchandise": {
      "merchandiseLines": [{
        "stableId": %s,
        "merchandise": {
          "productVariantReference": {
            "id": "gid://shopify/ProductVariantMerchandise/%s",
            "variantId": "gid://shopify/ProductVariant/%s",
            "properties": [], "sellingPlanId": null, "sellingPlanDigest": null
          }
        },
        "quantity": {"items": {"value": 1}},
        "expectedTotalPrice": {"any": true},
        "lineComponentsSource": null,
        "lineComponents": []
      }]
    },
    "memberships": {"memberships": []},
    "payment": {
      "totalAmount": {"any": true},
      "paymentLines": [],
      "billingAddress": {
        "streetAddress": {
          "address1": "", "city": "", "countryCode": "US",
          "lastName": "", "phone": ""
        }
      }
    },
    "buyerIdentity": {
      "customer": {"presentmentCurrency": "USD", "countryCode": "US"},
      "phoneCountryCode": "US",
      "marketingConsent": [],
      "shopPayOptInPhone": {"countryCode": "US"},
      "rememberMe": false
    },
    "tip": {"tipLines": []},
    "poNumber": null,
    "taxes": {
      "proposedAllocations": null,
      "proposedTotalAmount": {"any": true},
      "proposedTotalIncludedAmount": null,
      "proposedMixedStateTotalAmount": null,
      "proposedExemptions": []
    },
    "note": {"message": null, "customAttributes": []},
    "localizationExtension": {"fields": []},
    "nonNegotiableTerms": null,
    "scriptFingerprint": {
      "signature": null, "signatureUuid": null,
      "lineItemScriptChanges": [], "paymentScriptChanges": [], "shippingScriptChanges": []
    },
    "optionalDuties": {"buyerRefusesDuties": false},
    "cartMetafields": []
  },
  "operationName": "Proposal",
  "id": %s
}`,
		strconv.Quote(cs.SessionToken),
		strconv.Quote(cs.StableID), cs.VariantID, cs.VariantID,
		strconv.Quote(cs.ProposalID))
	return cs.patchPayload(p)
}

// buildProposal2 builds the email proposal payload (with queueToken + email)
func (cs *CheckoutSession) buildProposal2(queueToken, email string) string {
	p := fmt.Sprintf(`{
  "variables": {
    "sessionInput": {"sessionToken": %s},
    "queueToken": %s,
    "discounts": {"lines": [], "acceptUnexpectedDiscounts": true},
    "delivery": {
      "deliveryLines": [{
        "destination": {
          "partialStreetAddress": {
            "address1": "", "city": "", "countryCode": "US",
            "lastName": "", "phone": "", "oneTimeUse": false
          }
        },
        "selectedDeliveryStrategy": {
          "deliveryStrategyMatchingConditions": {
            "estimatedTimeInTransit": {"any": true},
            "shipments": {"any": true}
          },
          "options": {}
        },
        "targetMerchandiseLines": {"any": true},
        "deliveryMethodTypes": ["SHIPPING"],
        "expectedTotalPrice": {"any": true},
        "destinationChanged": true
      }],
      "noDeliveryRequired": [],
      "useProgressiveRates": false,
      "prefetchShippingRatesStrategy": null,
      "supportsSplitShipping": true
    },
    "deliveryExpectations": {"deliveryExpectationLines": []},
    "merchandise": {
      "merchandiseLines": [{
        "stableId": %s,
        "merchandise": {
          "productVariantReference": {
            "id": "gid://shopify/ProductVariantMerchandise/%s",
            "variantId": "gid://shopify/ProductVariant/%s",
            "properties": [], "sellingPlanId": null, "sellingPlanDigest": null
          }
        },
        "quantity": {"items": {"value": 1}},
        "expectedTotalPrice": {"any": true},
        "lineComponentsSource": null,
        "lineComponents": []
      }]
    },
    "memberships": {"memberships": []},
    "payment": {
      "totalAmount": {"any": true},
      "paymentLines": [],
      "billingAddress": {
        "streetAddress": {
          "address1": "", "city": "", "countryCode": "US",
          "lastName": "", "phone": ""
        }
      }
    },
    "buyerIdentity": {
      "customer": {"presentmentCurrency": "USD", "countryCode": "US"},
      "email": %s,
      "emailChanged": true,
      "phoneCountryCode": "US",
      "marketingConsent": [],
      "shopPayOptInPhone": {"countryCode": "US"},
      "rememberMe": false
    },
    "tip": {"tipLines": []},
    "poNumber": null,
    "taxes": {
      "proposedAllocations": null,
      "proposedTotalAmount": {"any": true},
      "proposedTotalIncludedAmount": null,
      "proposedMixedStateTotalAmount": null,
      "proposedExemptions": []
    },
    "note": {"message": null, "customAttributes": []},
    "localizationExtension": {"fields": []},
    "nonNegotiableTerms": null,
    "scriptFingerprint": {
      "signature": null, "signatureUuid": null,
      "lineItemScriptChanges": [], "paymentScriptChanges": [], "shippingScriptChanges": []
    },
    "optionalDuties": {"buyerRefusesDuties": false},
    "cartMetafields": []
  },
  "operationName": "Proposal",
  "id": %s
}`,
		strconv.Quote(cs.SessionToken), strconv.Quote(queueToken),
		strconv.Quote(cs.StableID), cs.VariantID, cs.VariantID,
		strconv.Quote(email),
		strconv.Quote(cs.ProposalID))
	return cs.patchPayload(p)
}

// buildProposal3 builds the full address proposal payload
func (cs *CheckoutSession) buildProposal3(queueToken, email string) string {
	addr := cs.Addr
	country := addr.Country
	if country == "" {
		country = "US"
	}
	province := addr.Province
	phone := addr.Phone
	if phone == "" {
		phone = "+12125550100"
	}

	p := fmt.Sprintf(`{
  "variables": {
    "sessionInput": {"sessionToken": %s},
    "queueToken": %s,
    "discounts": {"lines": [], "acceptUnexpectedDiscounts": true},
    "delivery": {
      "deliveryLines": [{
        "destination": {
          "partialStreetAddress": {
            "address1": %s, "address2": "",
            "city": %s, "countryCode": %s,
            "postalCode": %s, "firstName": %s,
            "lastName": %s, "zoneCode": %s,
            "phone": %s, "oneTimeUse": false
          }
        },
        "selectedDeliveryStrategy": {
          "deliveryStrategyMatchingConditions": {
            "estimatedTimeInTransit": {"any": true},
            "shipments": {"any": true}
          },
          "options": {}
        },
        "targetMerchandiseLines": {"any": true},
        "deliveryMethodTypes": ["SHIPPING"],
        "expectedTotalPrice": {"any": true},
        "destinationChanged": true
      }],
      "noDeliveryRequired": [],
      "useProgressiveRates": false,
      "prefetchShippingRatesStrategy": null,
      "supportsSplitShipping": true
    },
    "deliveryExpectations": {"deliveryExpectationLines": []},
    "merchandise": {
      "merchandiseLines": [{
        "stableId": %s,
        "merchandise": {
          "productVariantReference": {
            "id": "gid://shopify/ProductVariantMerchandise/%s",
            "variantId": "gid://shopify/ProductVariant/%s",
            "properties": [], "sellingPlanId": null, "sellingPlanDigest": null
          }
        },
        "quantity": {"items": {"value": 1}},
        "expectedTotalPrice": {"any": true},
        "lineComponentsSource": null,
        "lineComponents": []
      }]
    },
    "memberships": {"memberships": []},
    "payment": {
      "totalAmount": {"any": true},
      "paymentLines": [],
      "billingAddress": {
        "streetAddress": {
          "address1": %s, "address2": "",
          "city": %s, "countryCode": %s,
          "postalCode": %s, "firstName": %s,
          "lastName": %s, "zoneCode": %s,
          "phone": %s
        }
      }
    },
    "buyerIdentity": {
      "customer": {"presentmentCurrency": "USD", "countryCode": "US"},
      "email": %s,
      "emailChanged": false,
      "phoneCountryCode": "US",
      "marketingConsent": [],
      "shopPayOptInPhone": {"countryCode": "US"},
      "rememberMe": false
    },
    "tip": {"tipLines": []},
    "poNumber": null,
    "taxes": {
      "proposedAllocations": null,
      "proposedTotalAmount": {"any": true},
      "proposedTotalIncludedAmount": null,
      "proposedMixedStateTotalAmount": null,
      "proposedExemptions": []
    },
    "note": {"message": null, "customAttributes": []},
    "localizationExtension": {"fields": []},
    "nonNegotiableTerms": null,
    "scriptFingerprint": {
      "signature": null, "signatureUuid": null,
      "lineItemScriptChanges": [], "paymentScriptChanges": [], "shippingScriptChanges": []
    },
    "optionalDuties": {"buyerRefusesDuties": false},
    "cartMetafields": []
  },
  "operationName": "Proposal",
  "id": %s
}`,
		strconv.Quote(cs.SessionToken), strconv.Quote(queueToken),
		strconv.Quote(addr.Address1), strconv.Quote(addr.City), strconv.Quote(country),
		strconv.Quote(addr.Zip), strconv.Quote(addr.FirstName),
		strconv.Quote(addr.LastName), strconv.Quote(province),
		strconv.Quote(phone),
		strconv.Quote(cs.StableID), cs.VariantID, cs.VariantID,
		strconv.Quote(addr.Address1), strconv.Quote(addr.City), strconv.Quote(country),
		strconv.Quote(addr.Zip), strconv.Quote(addr.FirstName),
		strconv.Quote(addr.LastName), strconv.Quote(province),
		strconv.Quote(phone),
		strconv.Quote(email),
		strconv.Quote(cs.ProposalID))
	return cs.patchPayload(p)
}

// ─── Step 4: Submit for completion ───────────────────────────────────────────

type SubmitResult struct {
	ReceiptID string
	Code      string
	Message   string
	Response  map[string]any
	Total     string
}

func (cs *CheckoutSession) Step4Submit() SubmitResult {
	fmt.Println("[4/5] Submitting for completion...")

	time.Sleep(jitter(100, 200))

	addr := cs.Addr
	country := addr.Country
	if country == "" {
		country = "US"
	}
	province := addr.Province
	phone := addr.Phone
	if phone == "" {
		phone = "+12125550100"
	}
	email := addr.Email

	var handleLines []string
	for _, exp := range cs.DeliveryExps {
		h := exp["signedHandle"]
		if h != "" {
			handleLines = append(handleLines, fmt.Sprintf(`{"signedHandle":%s}`, strconv.Quote(h)))
		}
	}
	signedHandlesJSON := "[]"
	if len(handleLines) > 0 {
		signedHandlesJSON = "[" + strings.Join(handleLines, ",") + "]"
	}

	totalAmount := cs.ActualTotal
	if totalAmount == "" {
		totalAmount = "0.00"
	}

	currency := cs.CurrencyCode
	if currency == "" {
		currency = "USD"
	}

	attemptToken := generateAttemptToken(cs.CheckoutToken)
	pageID := generatePageID()

	gqlPayload := fmt.Sprintf(`{
  "variables": {
    "input": {
      "sessionInput": {"sessionToken": %s},
      "queueToken": %s,
      "discounts": {"lines": [], "acceptUnexpectedDiscounts": true},
      "delivery": {
        "deliveryLines": [{
          "destination": {
            "streetAddress": {
              "address1": %s, "address2": "",
              "city": %s, "countryCode": %s,
              "postalCode": %s, "firstName": %s,
              "lastName": %s, "zoneCode": %s,
              "phone": %s, "oneTimeUse": false
            }
          },
          "selectedDeliveryStrategy": {
            "deliveryStrategyByHandle": {
              "handle": %s,
              "customDeliveryRate": false
            },
            "options": {}
          },
          "targetMerchandiseLines": {
            "lines": [{"stableId": %s}]
          },
          "deliveryMethodTypes": ["SHIPPING"],
          "expectedTotalPrice": {"any": true},
          "destinationChanged": false
        }],
        "noDeliveryRequired": [],
        "useProgressiveRates": false,
        "prefetchShippingRatesStrategy": null,
        "supportsSplitShipping": true
      },
      "deliveryExpectations": {
        "deliveryExpectationLines": %s
      },
      "merchandise": {
        "merchandiseLines": [{
          "stableId": %s,
          "merchandise": {
            "productVariantReference": {
              "id": "gid://shopify/ProductVariantMerchandise/%s",
              "variantId": "gid://shopify/ProductVariant/%s",
              "properties": [], "sellingPlanId": null, "sellingPlanDigest": null
            }
          },
          "quantity": {"items": {"value": 1}},
          "expectedTotalPrice": {"any": true},
          "lineComponentsSource": null,
          "lineComponents": []
        }]
      },
      "memberships": {"memberships": []},
      "payment": {
        "totalAmount": {"value": {"amount": %s, "currencyCode": %s}},
        "paymentLines": [{
          "paymentMethod": {
            "directPaymentMethod": {
              "sessionId": %s,
              "billingAddress": {
                "streetAddress": {
                  "address1": %s, "address2": "",
                  "city": %s, "countryCode": %s,
                  "postalCode": %s, "firstName": %s,
                  "lastName": %s, "zoneCode": %s,
                  "phone": %s
                }
              },
              "cardSource": null
            },
            "giftCardPaymentMethod": null,
            "redeemablePaymentMethod": null,
            "walletPaymentMethod": null,
            "walletsPlatformPaymentMethod": null,
            "localPaymentMethod": null,
            "paymentOnDeliveryMethod": null,
            "paymentOnDeliveryMethod2": null,
            "manualPaymentMethod": null,
            "customPaymentMethod": null,
            "offsitePaymentMethod": null,
            "customOnsitePaymentMethod": null,
            "deferredPaymentMethod": null,
            "customerCreditCardPaymentMethod": null,
            "paypalBillingAgreementPaymentMethod": null,
            "remotePaymentInstrument": null
          },
          "amount": {"value": {"amount": %s, "currencyCode": %s}}
        }],
        "billingAddress": {
          "streetAddress": {
            "address1": %s, "address2": "",
            "city": %s, "countryCode": %s,
            "postalCode": %s, "firstName": %s,
            "lastName": %s, "zoneCode": %s,
            "phone": %s
          }
        }
      },
      "buyerIdentity": {
        "customer": {"presentmentCurrency": %s, "countryCode": "US"},
        "email": %s,
        "emailChanged": false,
        "phoneCountryCode": "US",
        "marketingConsent": [],
        "shopPayOptInPhone": {"countryCode": "US"},
        "rememberMe": false
      },
      "tip": {"tipLines": []},
      "taxes": {
        "proposedAllocations": null,
        "proposedTotalAmount": {"any": true},
        "proposedTotalIncludedAmount": null,
        "proposedMixedStateTotalAmount": null,
        "proposedExemptions": []
      },
      "note": {"message": null, "customAttributes": []},
      "localizationExtension": {"fields": []},
      "nonNegotiableTerms": null,
      "scriptFingerprint": {
        "signature": null, "signatureUuid": null,
        "lineItemScriptChanges": [], "paymentScriptChanges": [], "shippingScriptChanges": []
      },
      "optionalDuties": {"buyerRefusesDuties": false},
      "cartMetafields": []
    },
    "attemptToken": %s,
    "metafields": [],
    "analytics": {
      "requestUrl": %s,
      "pageId": %s
    }
  },
  "operationName": "SubmitForCompletion",
  "id": %s
}`,
		strconv.Quote(cs.SessionToken), strconv.Quote(cs.QueueToken),
		strconv.Quote(addr.Address1), strconv.Quote(addr.City), strconv.Quote(country),
		strconv.Quote(addr.Zip), strconv.Quote(addr.FirstName),
		strconv.Quote(addr.LastName), strconv.Quote(province),
		strconv.Quote(phone),
		strconv.Quote(cs.ShippingHandle),
		strconv.Quote(cs.StableID),
		signedHandlesJSON,
		strconv.Quote(cs.StableID), cs.VariantID, cs.VariantID,
		strconv.Quote(totalAmount), strconv.Quote(currency),
		strconv.Quote(cs.CardSessionID),
		strconv.Quote(addr.Address1), strconv.Quote(addr.City), strconv.Quote(country),
		strconv.Quote(addr.Zip), strconv.Quote(addr.FirstName),
		strconv.Quote(addr.LastName), strconv.Quote(province),
		strconv.Quote(phone),
		strconv.Quote(totalAmount), strconv.Quote(currency),
		strconv.Quote(addr.Address1), strconv.Quote(addr.City), strconv.Quote(country),
		strconv.Quote(addr.Zip), strconv.Quote(addr.FirstName),
		strconv.Quote(addr.LastName), strconv.Quote(province),
		strconv.Quote(phone),
		strconv.Quote(currency), strconv.Quote(email),
		strconv.Quote(attemptToken), strconv.Quote(cs.CheckoutURL), strconv.Quote(pageID),
		strconv.Quote(cs.SubmitID))

	gqlPayload = cs.patchPayload(gqlPayload)

	gqlURL := cs.ShopURL + "/checkouts/internal/graphql/persisted?operationName=SubmitForCompletion"

	req, _ := http.NewRequest("POST", gqlURL, strings.NewReader(gqlPayload))
	req.Header = cs.graphqlHeaders()

	resp, err := cs.Client.Do(req)
	if err != nil {
		return SubmitResult{Code: "HTTP_ERROR", Message: err.Error(), Total: cs.ActualTotal}
	}
	decompressBody(resp)
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return SubmitResult{Code: fmt.Sprintf("HTTP_%d", resp.StatusCode), Message: fmt.Sprintf("HTTP %d", resp.StatusCode), Total: cs.ActualTotal}
	}

	var response map[string]any
	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &response); err != nil {
		return SubmitResult{Code: "JSON_ERROR", Message: err.Error(), Total: cs.ActualTotal}
	}

	result := getMap(response, "data")
	submitResult := getMap(result, "submitForCompletion")
	resultType := getString(submitResult, "__typename")
	fmt.Printf("  Submit result: %s\n", resultType)

	switch resultType {
	case "SubmitSuccess", "SubmitAlreadyAccepted", "SubmittedForCompletion":
		receipt := getMap(submitResult, "receipt")
		receiptID := getString(receipt, "id")
		bodyStr := string(body)
		re := regexp.MustCompile(`"sessionToken"\s*:\s*"([^"]+)"`)
		if m := re.FindStringSubmatch(bodyStr); len(m) > 1 {
			cs.ReceiptSessionToken = m[1]
		}
		if receiptID != "" {
			fmt.Printf("  Receipt ID: %s\n", receiptID)
			return SubmitResult{ReceiptID: receiptID, Code: "SUBMIT_SUCCESS", Response: response, Total: cs.ActualTotal}
		}
		return SubmitResult{ReceiptID: "ACCEPTED", Code: "SUBMIT_ACCEPTED", Response: response, Total: cs.ActualTotal}

	case "SubmitRejected":
		errors := getSlice(submitResult, "errors")
		var codes, msgs []string
		for _, e := range errors {
			em, _ := e.(map[string]any)
			code := getString(em, "code")
			msg := getString(em, "localizedMessage")
			codes = append(codes, code)
			msgs = append(msgs, msg)
			fmt.Printf("  [REJECTED] %s: %s\n", code, msg)
		}
		primaryCode := "SUBMIT_REJECTED"
		if len(codes) > 0 {
			primaryCode = codes[0]
		}
		return SubmitResult{Code: primaryCode, Message: strings.Join(msgs, " | "), Response: response, Total: cs.ActualTotal}

	case "SubmitFailed":
		reason := getString(submitResult, "reason")
		return SubmitResult{Code: "SUBMIT_FAILED", Message: reason, Response: response, Total: cs.ActualTotal}

	case "Throttled":
		return SubmitResult{Code: "THROTTLED", Message: "Throttled by Shopify", Response: response, Total: cs.ActualTotal}

	default:
		return SubmitResult{Code: "UNEXPECTED_RESULT", Message: resultType, Response: response, Total: cs.ActualTotal}
	}
}

// ─── Step 5: Poll for receipt (GET-based, V2) ───────────────────────────────

func (cs *CheckoutSession) Step5PollReceipt(receiptID string) (bool, string, map[string]any) {
	fmt.Println("[5/5] Polling receipt...")

	if !strings.HasPrefix(receiptID, "gid://shopify/") {
		return false, `"code": "INVALID_RECEIPT_ID"`, nil
	}

	sessionToken := cs.ReceiptSessionToken
	if sessionToken == "" {
		sessionToken = cs.SessionToken
	}

	gqlURL := cs.ShopURL + "/checkouts/internal/graphql/persisted"

	var lastResponse map[string]any
	errorStrikes := 0

	for attempt := 1; attempt <= cfg.PollReceiptMax; attempt++ {
		fmt.Printf("  Poll %d/%d...\n", attempt, cfg.PollReceiptMax)

		varsJSON := fmt.Sprintf(`{"receiptId":%s,"sessionToken":%s}`,
			strconv.Quote(receiptID), strconv.Quote(sessionToken))

		params := url.Values{}
		params.Set("operationName", "PollForReceipt")
		params.Set("variables", varsJSON)
		params.Set("id", cs.PollForReceiptID)

		fullURL := gqlURL + "?" + params.Encode()

		req, err := http.NewRequest("GET", fullURL, nil)
		if err != nil {
			time.Sleep(cfg.ShortSleep)
			continue
		}
		req.Header = cs.graphqlHeadersPoll()

		resp, err := cs.Client.Do(req)
		if err != nil {
			time.Sleep(cfg.ShortSleep)
			continue
		}
		decompressBody(resp)

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != 200 {
			fmt.Printf("  [ERROR] HTTP %d\n", resp.StatusCode)
			time.Sleep(cfg.ShortSleep)
			continue
		}

		var response map[string]any
		if err := json.Unmarshal(body, &response); err != nil {
			time.Sleep(cfg.ShortSleep)
			continue
		}
		lastResponse = response

		if _, hasErrors := response["errors"]; hasErrors {
			if _, hasData := response["data"]; !hasData {
				errorStrikes++
				if errorStrikes >= 2 {
					return false, `"code": "UNKNOWN"`, response
				}
				time.Sleep(cfg.ShortSleep)
				continue
			}
		}

		receipt := navigateMap(response, "data", "receipt")
		rType := getString(receipt, "__typename")

		switch rType {
		case "ProcessedReceipt":
			fmt.Println("  Order completed (ProcessedReceipt)")
			return true, "SUCCESS", response

		case "ActionRequiredReceipt":
			fmt.Println("  3-D Secure or action required")
			return false, `"code": "ACTION_REQUIRED"`, response

		case "FailedReceipt":
			code := extractFailureCode(receipt)
			fmt.Printf("  FailedReceipt: %s\n", code)
			return false, code, response

		case "ProcessingReceipt", "":
			pollDelay := getFloat(receipt, "pollDelay")
			if pollDelay == 0 {
				pollDelay = 2000
			}
			waitSec := pollDelay / 1000.0
			if waitSec > cfg.MaxWaitSeconds {
				waitSec = cfg.MaxWaitSeconds
			}
			fmt.Printf("  Processing... (%.1fs)\n", waitSec)
			time.Sleep(time.Duration(waitSec * float64(time.Second)))

		default:
			time.Sleep(cfg.ShortSleep)
		}
	}

	fmt.Println("  Poll timeout")
	if lastResponse != nil {
		return false, `"code": "UNKNOWN"`, lastResponse
	}
	return false, `"code": "TIMEOUT"`, nil
}

// ─── Session token extraction (multiple patterns) ────────────────────────────

var sessionTokenPatterns = []*regexp.Regexp{
	regexp.MustCompile(`<meta\s+name="serialized-sessionToken"\s+content="([^"]+)"`),
	regexp.MustCompile(`<meta\s+name="serialized-session-token"\s+content="([^"]+)"`),
	regexp.MustCompile(`(?i)<meta\s+name="[^"]*session[^"]*token[^"]*"\s+content="([^"]+)"`),
	regexp.MustCompile(`(?i)serialized-sessionToken["'\s]*:\s*["']([^"']+)["']`),
	regexp.MustCompile(`(?i)sessionToken["'\s]*:\s*["']([^"']+)["']`),
}

func extractSessionToken(checkoutHTML string) string {
	for _, re := range sessionTokenPatterns {
		m := re.FindStringSubmatch(checkoutHTML)
		if m != nil && len(m[1]) > 50 {
			token := strings.Trim(m[1], `"'`)
			return htmlUnescape(token)
		}
	}
	return ""
}

// ─── Build ID extraction (fallback for commitSha) ───────────────────────────

var buildIDPatterns = []*regexp.Regexp{
	regexp.MustCompile(`/_next/static/([a-zA-Z0-9_-]{8,64})/_buildManifest\.js`),
	regexp.MustCompile(`"buildId"\s*:\s*"([a-zA-Z0-9_-]{8,64})"`),
	regexp.MustCompile(`/_next/static/([a-zA-Z0-9_-]{8,64})/`),
}

func extractBuildID(checkoutHTML string) string {
	for _, re := range buildIDPatterns {
		m := re.FindStringSubmatch(checkoutHTML)
		if m != nil {
			return m[1]
		}
	}
	return ""
}

// ─── Failure code extraction ─────────────────────────────────────────────────

func extractFailureCode(receipt map[string]any) string {
	pe := getMap(receipt, "processingError")
	code := getString(pe, "code")
	if code != "" {
		return code
	}
	return "UNKNOWN"
}

// ─── Map / JSON navigation helpers ───────────────────────────────────────────

func navigateMap(m map[string]any, keys ...string) map[string]any {
	current := m
	for _, k := range keys {
		next, ok := current[k].(map[string]any)
		if !ok {
			return nil
		}
		current = next
	}
	return current
}

func getMap(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	v, ok := m[key].(map[string]any)
	if !ok {
		return nil
	}
	return v
}

func getString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if ok {
		return s
	}
	if n, ok := v.(json.Number); ok {
		return n.String()
	}
	return fmt.Sprintf("%v", v)
}

func getFloat(m map[string]any, key string) float64 {
	if m == nil {
		return 0
	}
	v, ok := m[key]
	if !ok {
		return 0
	}
	switch val := v.(type) {
	case float64:
		return val
	case json.Number:
		f, _ := val.Float64()
		return f
	case int:
		return float64(val)
	}
	return 0
}

func getBool(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	v, ok := m[key].(bool)
	return ok && v
}

func getSlice(m map[string]any, key string) []any {
	if m == nil {
		return nil
	}
	v, ok := m[key].([]any)
	if !ok {
		return nil
	}
	return v
}

// ─── String helpers ──────────────────────────────────────────────────────────

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func onlyDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func htmlUnescape(s string) string {
	replacer := strings.NewReplacer(
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", `"`,
		"&#39;", "'",
		"&#x27;", "'",
		"&#x2F;", "/",
	)
	return replacer.Replace(s)
}

func parsePrice(s string) float64 {
	s = strings.TrimSpace(s)
	s = strings.TrimLeft(s, "$")
	var f float64
	fmt.Sscanf(s, "%f", &f)
	return f
}

func formatAmount(amount string) string {
	if amount == "" {
		return "$0"
	}
	if strings.HasPrefix(amount, "$") {
		return amount
	}
	return "$" + amount
}
