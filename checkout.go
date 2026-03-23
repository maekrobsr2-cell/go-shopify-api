package main

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/google/uuid"
)

// decompressBody wraps resp.Body with the appropriate decompressor
// based on the Content-Encoding header. Must be called before reading
// the body whenever we explicitly set Accept-Encoding in the request.
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

// ─── GraphQL header builder ──────────────────────────────────────────────────

func (cs *CheckoutSession) graphqlHeaders() http.Header {
	parsed, _ := url.Parse(cs.ShopURL)
	origin := fmt.Sprintf("%s://%s", parsed.Scheme, parsed.Host)

	h := http.Header{}
	h.Set("Accept", "application/json")
	h.Set("Accept-Language", "en-US,en;q=0.9")
	h.Set("Accept-Encoding", "gzip, deflate, br")
	h.Set("Content-Type", "application/json")
	h.Set("Origin", origin)
	h.Set("Referer", fmt.Sprintf("%s/checkouts/cn/%s", origin, cs.CheckoutToken))
	h.Set("Sec-CH-UA", cs.FP.SecCHUA)
	h.Set("Sec-CH-UA-Mobile", cs.FP.SecCHUAMobile)
	h.Set("Sec-CH-UA-Platform", cs.FP.SecCHUAPlatform)
	h.Set("Sec-Fetch-Dest", "empty")
	h.Set("Sec-Fetch-Mode", "cors")
	h.Set("Sec-Fetch-Site", "same-origin")
	h.Set("User-Agent", cs.FP.UserAgent)
	h.Set("shopify-checkout-client", "checkout-web/1.0")
	h.Set("shopify-checkout-source", fmt.Sprintf(`id="%s", type="cn"`, cs.CheckoutToken))
	h.Set("x-checkout-web-source-id", cs.CheckoutToken)
	h.Set("x-checkout-one-session-token", cs.SessionToken)
	if cs.BuildID != "" {
		h.Set("x-checkout-web-build-id", cs.BuildID)
	}
	return h
}

// ─── Test / Bogus Gateway Detection ─────────────────────────────────────────
// Matches neww.py's _TEST_MODE_PATTERNS: scan checkout HTML for Shopify
// test-mode / bogus-gateway indicators. Only scan first 50 KB.

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

func detectTestMode(html string) bool {
	if len(html) == 0 {
		return false
	}
	sample := html
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

// ─── Session warming: visit storefront before checkout ──────────────────────
// Matches Python's warm_storefront_session(): visits homepage, /collections,
// and /cart.js to build a realistic cookie/session profile. Without this,
// Shopify is more likely to trigger CAPTCHAs or reject the checkout.

func (cs *CheckoutSession) WarmStorefrontSession() {
	// Step 1: Visit homepage (sets _shopify_y, _shopify_s cookies)
	fmt.Println("  [WARM] Visiting storefront...")
	req, err := http.NewRequest("GET", cs.ShopURL, nil)
	if err == nil {
		setBrowseHeaders(req, cs.FP, cs.ShopURL)
		resp, err := cs.Client.Do(req)
		if err == nil {
			resp.Body.Close()
		}
	}

	time.Sleep(jitter(300, 700))

	// Step 2: Visit collections page (builds referrer chain)
	parsed, _ := url.Parse(cs.ShopURL)
	origin := fmt.Sprintf("%s://%s", parsed.Scheme, parsed.Host)
	req, err = http.NewRequest("GET", cs.ShopURL+"/collections", nil)
	if err == nil {
		setBrowseHeaders(req, cs.FP, cs.ShopURL)
		req.Header.Set("Referer", origin+"/")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		resp, err := cs.Client.Do(req)
		if err == nil {
			resp.Body.Close()
		}
	}

	time.Sleep(jitter(200, 500))

	// Step 3: Hit cart.js to initialize cart cookie
	req, err = http.NewRequest("GET", cs.ShopURL+"/cart.js", nil)
	if err == nil {
		setBrowseHeaders(req, cs.FP, cs.ShopURL)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Sec-Fetch-Dest", "empty")
		req.Header.Set("Sec-Fetch-Mode", "cors")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		resp, err := cs.Client.Do(req)
		if err == nil {
			resp.Body.Close()
		}
	}
}

// ─── Step 1: Add to cart & create checkout ───────────────────────────────────

func (cs *CheckoutSession) Step1AddToCart() error {
	fmt.Println("[1/5] Adding to cart and creating checkout...")

	// Add to cart
	payload, _ := json.Marshal(map[string]any{
		"id":       cs.VariantID,
		"quantity": 1,
	})

	time.Sleep(jitter(150, 400))

	req, err := http.NewRequest("POST", cs.ShopURL+"/cart/add.js", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("cart/add.js request build: %w", err)
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
	resp.Body.Close()

	if resp.StatusCode == 429 {
		return fmt.Errorf("429 rate limited on cart/add")
	}
	fmt.Printf("  Add to cart: %d\n", resp.StatusCode)

	time.Sleep(jitter(300, 600))

	// Get checkout
	checkoutReq, err := http.NewRequest("GET", cs.ShopURL+"/checkout", nil)
	if err != nil {
		return fmt.Errorf("checkout request build: %w", err)
	}
	setBrowseHeaders(checkoutReq, cs.FP, cs.ShopURL)

	checkoutResp, err := cs.Client.Do(checkoutReq)
	if err != nil {
		return fmt.Errorf("checkout GET: %w", err)
	}
	decompressBody(checkoutResp)
	defer checkoutResp.Body.Close()

	bodyBytes, _ := io.ReadAll(checkoutResp.Body)
	html := string(bodyBytes)
	finalURL := checkoutResp.Request.URL.String()

	// ── Test/bogus gateway detection (Layer 2) ──
	if detectTestMode(html) {
		fmt.Printf("  [TEST-DETECT] ⚠️  Bogus/test gateway detected in checkout HTML!\n")
		return fmt.Errorf("TEST_MODE_DETECTED")
	}

	fmt.Printf("  [DEBUG] Final URL: %s\n", finalURL)

	if !strings.Contains(finalURL, "/checkouts/cn/") {
		return fmt.Errorf("no checkout token in URL: %s", finalURL)
	}

	// Extract checkout token
	parts := strings.SplitAfter(finalURL, "/checkouts/cn/")
	if len(parts) < 2 {
		return fmt.Errorf("failed to parse checkout token from URL")
	}
	token := strings.Split(parts[1], "/")[0]
	token = strings.Split(token, "?")[0]
	cs.CheckoutToken = token
	fmt.Printf("  [OK] Checkout token: %s\n", token)

	// Extract session token
	cs.SessionToken = extractSessionToken(html)
	if cs.SessionToken == "" {
		return fmt.Errorf("session token not found in HTML")
	}
	fmt.Println("  [OK] Session token extracted")

	// Extract build ID (reduces CAPTCHA triggers)
	cs.BuildID = extractBuildID(html)
	if cs.BuildID != "" {
		fmt.Printf("  [OK] Build ID: %s...\n", truncate(cs.BuildID, 20))
	}

	return nil
}

// ─── Step 2: Tokenize credit card ────────────────────────────────────────────

func (cs *CheckoutSession) Step2TokenizeCard() error {
	fmt.Println("[2/5] Tokenizing credit card...")

	time.Sleep(jitter(200, 500))

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
		return fmt.Errorf("tokenize request build: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", "https://checkout.pci.shopifyinc.com")
	req.Header.Set("Referer", "https://checkout.pci.shopifyinc.com/")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-CH-UA", cs.FP.SecCHUA)
	req.Header.Set("Sec-CH-UA-Mobile", cs.FP.SecCHUAMobile)
	req.Header.Set("Sec-CH-UA-Platform", cs.FP.SecCHUAPlatform)
	req.Header.Set("User-Agent", cs.FP.UserAgent)

	// Use a standard client WITH proxy for the PCI endpoint (cross-origin, different host)
	tokenClient := newStandardClient(cs.ProxyURL, cfg.HTTPTimeoutShort)

	// Retry up to 3 times on 429 rate-limit with increasing backoff
	var resp *http.Response
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(attempt)*time.Second + jitter(500, 1500)
			fmt.Printf("  [RETRY] Tokenize attempt %d/3 after %v backoff\n", attempt+1, backoff)
			time.Sleep(backoff)
			// Rebuild request body since it was already consumed
			req, _ = http.NewRequest("POST", endpoint, bytes.NewReader(payload))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json")
			req.Header.Set("Origin", "https://checkout.pci.shopifyinc.com")
			req.Header.Set("Referer", "https://checkout.pci.shopifyinc.com/")
			req.Header.Set("Sec-Fetch-Site", "cross-site")
			req.Header.Set("Sec-Fetch-Mode", "cors")
			req.Header.Set("Sec-Fetch-Dest", "empty")
			req.Header.Set("Sec-CH-UA", cs.FP.SecCHUA)
			req.Header.Set("Sec-CH-UA-Mobile", cs.FP.SecCHUAMobile)
			req.Header.Set("Sec-CH-UA-Platform", cs.FP.SecCHUAPlatform)
			req.Header.Set("User-Agent", cs.FP.UserAgent)
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
		return fmt.Errorf("no card session ID returned (errors: %v)", tokenResp.Errors)
	}

	cs.CardSessionID = tokenResp.ID
	fmt.Printf("  [OK] Card session ID: %s\n", tokenResp.ID)
	return nil
}

// ─── Step 3: Submit initial proposal & poll for shipping ─────────────────────

func (cs *CheckoutSession) Step3Proposal() error {
	fmt.Println("[3/5] Submitting proposal...")

	time.Sleep(jitter(100, 300))

	cs.MerchandiseID = uuid.New().String()
	gqlURL := cs.ShopURL + "/checkouts/unstable/graphql?operationName=Proposal"

	payload := cs.buildProposalPayload(ProposalFullQuery, "any", "", false)
	payloadBytes, _ := json.Marshal(addAPQExtensions(payload))

	// Retry on 429
	var body []byte
	for attempt := range 3 {
		req, _ := http.NewRequest("POST", gqlURL, bytes.NewReader(payloadBytes))
		req.Header = cs.graphqlHeaders()
		resp, err := cs.Client.Do(req)
		if err != nil {
			return fmt.Errorf("proposal POST: %w", err)
		}
		decompressBody(resp)
		body, _ = io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == 429 {
			wait := 2.0 + float64(attempt)*2.0 + rand.Float64()
			fmt.Printf("  [RATE-LIMITED] 429 on proposal (attempt %d/3), waiting %.1fs...\n", attempt+1, wait)
			time.Sleep(time.Duration(wait * float64(time.Second)))
			continue
		}
		if resp.StatusCode != 200 {
			return fmt.Errorf("proposal HTTP %d", resp.StatusCode)
		}
		break
	}

	var response map[string]any
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("proposal JSON decode: %w", err)
	}

	// Check for GraphQL errors
	if errs, ok := response["errors"]; ok {
		return fmt.Errorf("GraphQL errors: %v", errs)
	}

	// Navigate response
	result := navigateMap(response, "data", "session", "negotiate", "result")
	if getString(result, "__typename") != "NegotiationResultAvailable" {
		return fmt.Errorf("proposal result not available: %v", getString(result, "__typename"))
	}

	cs.QueueToken = getString(result, "queueToken")
	sellerProposal := getMap(result, "sellerProposal")

	// Detect phone requirement
	cs.PhoneRequired = cs.detectPhoneRequired(sellerProposal)

	// Extract shipping handle
	deliveryTerms := getMap(sellerProposal, "delivery")
	if getString(deliveryTerms, "__typename") == "FilledDeliveryTerms" {
		cs.extractShipping(deliveryTerms)
	}

	// Extract total
	cs.extractTotal(sellerProposal)

	// Extract delivery expectations
	cs.extractDeliveryExpectations(sellerProposal)

	// If expectations are still pending, poll for them
	expTerms := getMap(sellerProposal, "deliveryExpectations")
	if getString(expTerms, "__typename") == "PendingTerms" {
		if getString(deliveryTerms, "__typename") == "PendingTerms" {
			// Both pending — full poll
			fmt.Println("  [INFO] Both delivery and expectations pending - polling...")
			cs.pollFullDelivery()
		} else if cs.ShippingHandle != "" {
			// Only expectations pending
			fmt.Printf("  [INFO] Polling expectations with handle: %s...\n", truncate(cs.ShippingHandle, 50))
			cs.pollExpectations()
		}
	}

	fmt.Printf("  [INFO] Phone Required: %v\n", cs.PhoneRequired)
	if cs.ShippingHandle == "" {
		return fmt.Errorf("no shipping handle obtained")
	}
	return nil
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

	time.Sleep(jitter(100, 300))

	gqlURL := cs.ShopURL + "/checkouts/unstable/graphql?operationName=SubmitForCompletion"
	attemptToken := fmt.Sprintf("%s-%s", cs.CheckoutToken, uuid.New().String()[:10])

	// Build delivery line config with full address (matches Python get_delivery_line_config)
	deliveryLine := cs.buildDeliveryLine(cs.ShippingHandle, cs.MerchandiseID, true, true, false)

	billingAddr := cs.billingAddress()

	// Delivery expectation lines (placed at TOP-LEVEL of input, NOT inside delivery block)
	var expLines []map[string]string
	for _, exp := range cs.DeliveryExps {
		expLines = append(expLines, map[string]string{"signedHandle": exp["signedHandle"]})
	}

	// Delivery block WITHOUT expectations (per Python reference)
	deliveryBlock := map[string]any{
		"deliveryLines":      []any{deliveryLine},
		"noDeliveryRequired": []any{},
	}

	// Payment amounts: use actual total+currency from proposal when available,
	// fall back to {any: true} when total is missing
	currency := cs.CurrencyCode
	if currency == "" {
		currency = "USD"
	}
	paymentTotalAmount := map[string]any{"any": true}
	paymentLineAmount := map[string]any{"any": true}
	if cs.ActualTotal != "" {
		totalF := parsePrice(cs.ActualTotal)
		if totalF > 0 {
			amountVal := map[string]any{"amount": totalF, "currencyCode": currency}
			paymentTotalAmount = map[string]any{"value": amountVal}
			paymentLineAmount = map[string]any{"value": amountVal}
		}
	}

	inputData := map[string]any{
		"sessionInput": map[string]any{"sessionToken": cs.SessionToken},
		"queueToken":   cs.QueueToken,
		"discounts":    map[string]any{"lines": []any{}, "acceptUnexpectedDiscounts": true},
		"delivery":     deliveryBlock,
		"merchandise":  cs.merchandiseBlock(false),
		"payment": map[string]any{
			"totalAmount": paymentTotalAmount,
			"paymentLines": []any{
				map[string]any{
					"paymentMethod": map[string]any{
						"directPaymentMethod": map[string]any{
							"sessionId":      cs.CardSessionID,
							"billingAddress": map[string]any{"streetAddress": billingAddr},
						},
					},
					"amount": paymentLineAmount,
				},
			},
			"billingAddress": map[string]any{"streetAddress": billingAddr},
		},
		"buyerIdentity": cs.buyerIdentity(),
		"taxes":         map[string]any{"proposedTotalAmount": map[string]any{"any": true}},
	}
	// deliveryExpectations at top-level of input (NOT inside delivery block), per Python reference
	if len(expLines) > 0 {
		inputData["deliveryExpectations"] = map[string]any{
			"deliveryExpectationLines": expLines,
		}
	}

	payload := addAPQExtensions(map[string]any{
		"operationName": "SubmitForCompletion",
		"query":         SubmitCompletionQuery,
		"variables": map[string]any{
			"attemptToken": attemptToken,
			"input":        inputData,
		},
	})

	payloadBytes, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", gqlURL, bytes.NewReader(payloadBytes))
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
	fmt.Printf("  [INFO] Result: %s\n", resultType)

	switch resultType {
	case "SubmitSuccess", "SubmitAlreadyAccepted", "SubmittedForCompletion":
		receipt := getMap(submitResult, "receipt")
		receiptID := getString(receipt, "id")
		if receiptID != "" {
			fmt.Printf("  [SUCCESS] Receipt ID: %s\n", receiptID)
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
			fmt.Printf("  [ERROR] %s: %s\n", code, msg)
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

// ─── Step 5: Poll for receipt ────────────────────────────────────────────────

func (cs *CheckoutSession) Step5PollReceipt(receiptID string) (bool, string, map[string]any) {
	fmt.Println("[5/5] Polling for receipt...")

	gqlURL := cs.ShopURL + "/checkouts/unstable/graphql?operationName=PollForReceipt"

	if !strings.HasPrefix(receiptID, "gid://shopify/") {
		return false, `"code": "INVALID_RECEIPT_ID"`, nil
	}

	payload := addAPQExtensions(map[string]any{
		"operationName": "PollForReceipt",
		"query":         PollReceiptQuery,
		"variables": map[string]any{
			"receiptId":    receiptID,
			"sessionToken": cs.SessionToken,
		},
	})

	var lastResponse map[string]any
	errorStrikes := 0

	for attempt := 1; attempt <= cfg.PollReceiptMax; attempt++ {
		fmt.Printf("  Polling %d/%d...\n", attempt, cfg.PollReceiptMax)

		payloadBytes, _ := json.Marshal(payload)
		req, _ := http.NewRequest("POST", gqlURL, bytes.NewReader(payloadBytes))
		req.Header = cs.graphqlHeaders()

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

		// Check for errors without data
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
			fmt.Println("  [SUCCESS] Order completed (ProcessedReceipt)")
			return true, "SUCCESS", response

		case "ActionRequiredReceipt":
			fmt.Println("  [ACTION REQUIRED] 3-D Secure or other action required")
			return false, `"code": "ACTION_REQUIRED"`, response

		case "FailedReceipt":
			fmt.Println("  [FAILED] FailedReceipt")
			code := extractFailureCode(receipt)
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
			fmt.Printf("  [INFO] Still processing; waiting %.2fs\n", waitSec)
			time.Sleep(time.Duration(waitSec * float64(time.Second)))

		default:
			time.Sleep(cfg.ShortSleep)
		}
	}

	fmt.Println("  [TIMEOUT] Poll attempts exhausted")
	if lastResponse != nil {
		return false, `"code": "UNKNOWN"`, lastResponse
	}
	return false, `"code": "TIMEOUT"`, nil
}

// ─── Proposal payload builder ────────────────────────────────────────────────

func (cs *CheckoutSession) buildProposalPayload(query string, shippingHandle string, stableID string, forPoll bool) map[string]any {
	deliveryLine := cs.buildDeliveryLine(shippingHandle, stableID, false, true, forPoll)

	deliveryBlock := map[string]any{
		"deliveryLines":      []any{deliveryLine},
		"noDeliveryRequired": []any{},
	}
	// Polls include supportsSplitShipping (matches Python poll functions)
	if forPoll {
		deliveryBlock["supportsSplitShipping"] = true
	}

	// Core 7 variables (sent for BOTH initial Step 3 AND polls)
	vars := map[string]any{
		"delivery":      deliveryBlock,
		"discounts":     map[string]any{"lines": []any{}, "acceptUnexpectedDiscounts": true},
		"payment":       map[string]any{"totalAmount": map[string]any{"any": true}, "paymentLines": []any{}, "billingAddress": map[string]any{"streetAddress": cs.billingAddress()}},
		"merchandise":   cs.merchandiseBlock(forPoll),
		"buyerIdentity": cs.buyerIdentity(),
		"taxes":         map[string]any{"proposedTotalAmount": map[string]any{"any": true}},
		"sessionInput":  map[string]any{"sessionToken": cs.SessionToken},
	}

	// Extra 6 variables ONLY for polls (matches Python: step3_proposal_ctx sends 7, polls send 13)
	if forPoll {
		vars["tip"] = map[string]any{"tipLines": []any{}}
		vars["note"] = map[string]any{"message": nil, "customAttributes": []any{}}
		vars["scriptFingerprint"] = map[string]any{
			"signature": nil, "signatureUuid": nil,
			"lineItemScriptChanges": []any{}, "paymentScriptChanges": []any{}, "shippingScriptChanges": []any{},
		}
		vars["optionalDuties"] = map[string]any{"buyerRefusesDuties": false}
		vars["cartMetafields"] = []any{}
		vars["memberships"] = map[string]any{"memberships": []any{}}
	}

	return map[string]any{
		"operationName": "Proposal",
		"query":         query,
		"variables":     vars,
	}
}

func (cs *CheckoutSession) buildDeliveryLine(handle string, stableID string, useFull bool, phoneRequired bool, forPoll bool) map[string]any {
	addrKey := "partialStreetAddress"
	if useFull {
		addrKey = "streetAddress"
	}

	addrData := map[string]any{
		"address1":    cs.Addr.Address1,
		"city":        cs.Addr.City,
		"countryCode": cs.Addr.Country,
		"firstName":   cs.Addr.FirstName,
		"lastName":    cs.Addr.LastName,
		"zoneCode":    cs.Addr.Province,
		"postalCode":  cs.Addr.Zip,
		"phone":       cs.Addr.Phone,
	}
	// Polls include oneTimeUse in partial address (matches Python inline poll delivery line)
	if forPoll && !useFull {
		addrData["oneTimeUse"] = false
	}

	// Only use stableID when explicitly provided; step3 initial uses {"any": true}
	targetLines := map[string]any{"any": true}
	if stableID != "" {
		targetLines = map[string]any{"lines": []any{map[string]any{"stableId": stableID}}}
	}

	strategy := map[string]any{
		"deliveryStrategyByHandle": map[string]any{
			"handle":             handle,
			"customDeliveryRate": false,
		},
	}
	// Non-poll calls include phone options (matches Python get_delivery_line_config)
	if phoneRequired && !forPoll {
		strategy["options"] = map[string]any{"phone": cs.Addr.Phone}
	}

	line := map[string]any{
		"destination":              map[string]any{addrKey: addrData},
		"targetMerchandiseLines":   targetLines,
		"deliveryMethodTypes":      []string{"SHIPPING"},
		"selectedDeliveryStrategy": strategy,
		"expectedTotalPrice":       map[string]any{"any": true},
	}
	// Polls include destinationChanged: false (matches Python inline poll delivery line)
	if forPoll {
		line["destinationChanged"] = false
	}

	return line
}

func (cs *CheckoutSession) merchandiseBlock(forPoll bool) map[string]any {
	pvRef := map[string]any{
		"id":         fmt.Sprintf("gid://shopify/ProductVariantMerchandise/%s", cs.VariantID),
		"variantId":  fmt.Sprintf("gid://shopify/ProductVariant/%s", cs.VariantID),
		"properties": []any{},
	}
	// Only polls include sellingPlanId (matches Python poll_for_delivery_and_expectations_ctx)
	if forPoll {
		pvRef["sellingPlanId"] = nil
	}

	lineItem := map[string]any{
		"stableId":           cs.MerchandiseID,
		"merchandise":        map[string]any{"productVariantReference": pvRef},
		"quantity":           map[string]any{"items": map[string]any{"value": 1}},
		"expectedTotalPrice": map[string]any{"any": true},
	}
	// Only polls include lineComponents (matches Python poll_for_delivery_and_expectations_ctx)
	if forPoll {
		lineItem["lineComponents"] = []any{}
	}

	return map[string]any{
		"merchandiseLines": []any{lineItem},
	}
}

func (cs *CheckoutSession) buyerIdentity() map[string]any {
	currency := "USD"
	if cs.CurrencyCode != "" {
		currency = cs.CurrencyCode
	}
	return map[string]any{
		"customer": map[string]any{"presentmentCurrency": currency, "countryCode": cs.Addr.Country},
		"email":    cs.Addr.Email,
	}
}

// buyerIdentitySubmit returns buyer identity with phone fields for Step 4 (SubmitForCompletion)
func (cs *CheckoutSession) buyerIdentitySubmit() map[string]any {
	currency := "USD"
	if cs.CurrencyCode != "" {
		currency = cs.CurrencyCode
	}
	bi := map[string]any{
		"customer": map[string]any{"presentmentCurrency": currency, "countryCode": cs.Addr.Country},
		"email":    cs.Addr.Email,
	}
	if cs.Addr.Phone != "" {
		bi["phoneCountryCode"] = cs.Addr.Country
		bi["shopPayOptInPhone"] = map[string]any{
			"number":      cs.Addr.Phone,
			"countryCode": cs.Addr.Country,
		}
	}
	return bi
}

func (cs *CheckoutSession) billingAddress() map[string]any {
	return map[string]any{
		"address1":    cs.Addr.Address1,
		"city":        cs.Addr.City,
		"countryCode": cs.Addr.Country,
		"postalCode":  cs.Addr.Zip,
		"firstName":   cs.Addr.FirstName,
		"lastName":    cs.Addr.LastName,
		"zoneCode":    cs.Addr.Province,
		"phone":       cs.Addr.Phone,
	}
}

// ─── Polling helpers ─────────────────────────────────────────────────────────

func (cs *CheckoutSession) pollFullDelivery() {
	gqlURL := cs.ShopURL + "/checkouts/unstable/graphql?operationName=Proposal"

	for attempt := range 5 {
		fmt.Printf("  [POLL] Full delivery attempt %d/5...\n", attempt+1)

		payload := cs.buildProposalPayload(ProposalFullQuery, cs.ShippingHandle, cs.MerchandiseID, true)
		if cs.ShippingHandle == "" {
			payload = cs.buildProposalPayload(ProposalFullQuery, "any", cs.MerchandiseID, true)
		}
		payloadBytes, _ := json.Marshal(addAPQExtensions(payload))

		req, _ := http.NewRequest("POST", gqlURL, bytes.NewReader(payloadBytes))
		req.Header = cs.graphqlHeaders()
		resp, err := cs.Client.Do(req)
		if err != nil {
			time.Sleep(cfg.ShortSleep)
			continue
		}
		decompressBody(resp)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != 200 {
			time.Sleep(cfg.ShortSleep)
			continue
		}

		var response map[string]any
		json.Unmarshal(body, &response)

		result := navigateMap(response, "data", "session", "negotiate", "result")
		if getString(result, "__typename") != "NegotiationResultAvailable" {
			time.Sleep(cfg.ShortSleep)
			continue
		}

		qt := getString(result, "queueToken")
		if qt != "" {
			cs.QueueToken = qt
		}

		sp := getMap(result, "sellerProposal")

		// Extract shipping
		dt := getMap(sp, "delivery")
		if getString(dt, "__typename") == "FilledDeliveryTerms" {
			cs.extractShipping(dt)
		}

		// Extract total
		cs.extractTotal(sp)

		// Extract expectations
		cs.extractDeliveryExpectations(sp)

		if cs.ShippingHandle != "" && len(cs.DeliveryExps) > 0 && cs.ActualTotal != "" {
			fmt.Printf("  [POLL] ✓ Complete! Handle: %s, Total: $%s\n", truncate(cs.ShippingHandle, 30), cs.ActualTotal)
			return
		}

		// Wait based on poll delay
		waitSec := min(0.5, cfg.MaxWaitSeconds)
		if getString(dt, "__typename") == "PendingTerms" {
			delay := getFloat(dt, "pollDelay")
			if delay > 0 {
				waitSec = min(delay/1000.0, cfg.MaxWaitSeconds)
			}
		}
		time.Sleep(time.Duration(waitSec * float64(time.Second)))
	}
}

func (cs *CheckoutSession) pollExpectations() {
	gqlURL := cs.ShopURL + "/checkouts/unstable/graphql?operationName=Proposal"

	for attempt := range 5 {
		fmt.Printf("  [POLL] Expectations attempt %d/5...\n", attempt+1)

		payload := cs.buildProposalPayload(ProposalPollQuery, cs.ShippingHandle, cs.MerchandiseID, true)
		payloadBytes, _ := json.Marshal(addAPQExtensions(payload))

		req, _ := http.NewRequest("POST", gqlURL, bytes.NewReader(payloadBytes))
		req.Header = cs.graphqlHeaders()
		resp, err := cs.Client.Do(req)
		if err != nil {
			time.Sleep(cfg.ShortSleep)
			continue
		}
		decompressBody(resp)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != 200 {
			time.Sleep(cfg.ShortSleep)
			continue
		}

		var response map[string]any
		json.Unmarshal(body, &response)

		result := navigateMap(response, "data", "session", "negotiate", "result")
		sp := getMap(result, "sellerProposal")
		expTerms := getMap(sp, "deliveryExpectations")

		if getString(expTerms, "__typename") == "FilledDeliveryExpectationTerms" {
			exps := getSlice(expTerms, "deliveryExpectations")
			cs.DeliveryExps = nil
			for _, e := range exps {
				em, _ := e.(map[string]any)
				sh := getString(em, "signedHandle")
				if sh != "" {
					cs.DeliveryExps = append(cs.DeliveryExps, map[string]string{"signedHandle": sh})
				}
			}
			qt := getString(result, "queueToken")
			if qt != "" {
				cs.QueueToken = qt
			}
			fmt.Printf("  [POLL] ✓ Got %d expectations\n", len(cs.DeliveryExps))
			return
		}

		if getString(expTerms, "__typename") == "PendingTerms" {
			delay := getFloat(expTerms, "pollDelay")
			if delay == 0 {
				delay = 2000
			}
			waitSec := min(delay/1000.0, cfg.MaxWaitSeconds)
			time.Sleep(time.Duration(waitSec * float64(time.Second)))
		} else {
			time.Sleep(cfg.ShortSleep)
		}
	}
}

// ─── Extraction helpers ──────────────────────────────────────────────────────

func (cs *CheckoutSession) extractShipping(deliveryTerms map[string]any) {
	lines := getSlice(deliveryTerms, "deliveryLines")
	if len(lines) == 0 {
		return
	}
	firstLine, _ := lines[0].(map[string]any)
	strategies := getSlice(firstLine, "availableDeliveryStrategies")
	if len(strategies) == 0 {
		return
	}
	first, _ := strategies[0].(map[string]any)
	cs.ShippingHandle = getString(first, "handle")
	amtConstraint := getMap(first, "amount")
	if getString(amtConstraint, "__typename") == "MoneyValueConstraint" {
		cs.ShippingAmount = getString(getMap(amtConstraint, "value"), "amount")
	}
	if cs.ShippingHandle != "" {
		fmt.Printf("  [OK] Shipping handle: %s\n", truncate(cs.ShippingHandle, 50))
	}
}

func (cs *CheckoutSession) extractTotal(sellerProposal map[string]any) {
	// Try runningTotal first (newer API), then checkoutTotal
	for _, key := range []string{"runningTotal", "checkoutTotal"} {
		ct := getMap(sellerProposal, key)
		if getString(ct, "__typename") == "MoneyValueConstraint" {
			v := getMap(ct, "value")
			amt := getString(v, "amount")
			curr := getString(v, "currencyCode")
			if amt != "" {
				cs.ActualTotal = amt
				if curr != "" {
					cs.CurrencyCode = curr
				}
				fmt.Printf("  [OK] Total: %s %s\n", amt, cs.CurrencyCode)
				return
			}
		}
	}
}

func (cs *CheckoutSession) extractDeliveryExpectations(sellerProposal map[string]any) {
	expTerms := getMap(sellerProposal, "deliveryExpectations")
	if getString(expTerms, "__typename") == "FilledDeliveryExpectationTerms" {
		exps := getSlice(expTerms, "deliveryExpectations")
		cs.DeliveryExps = nil
		for _, e := range exps {
			em, _ := e.(map[string]any)
			sh := getString(em, "signedHandle")
			if sh != "" {
				cs.DeliveryExps = append(cs.DeliveryExps, map[string]string{"signedHandle": sh})
			}
		}
		fmt.Printf("  [OK] Found %d expectations\n", len(cs.DeliveryExps))
	}
}

func (cs *CheckoutSession) detectPhoneRequired(sellerProposal map[string]any) bool {
	deliveryTerms := getMap(sellerProposal, "delivery")
	if getString(deliveryTerms, "__typename") != "FilledDeliveryTerms" {
		return true // default to true for safety
	}
	lines := getSlice(deliveryTerms, "deliveryLines")
	for _, l := range lines {
		lm, _ := l.(map[string]any)
		strategies := getSlice(lm, "availableDeliveryStrategies")
		for _, s := range strategies {
			sm, _ := s.(map[string]any)
			if getBool(sm, "phoneRequired") {
				fmt.Println("  [DETECT] ✓ Phone number IS required")
				return true
			}
		}
	}
	fmt.Println("  [DETECT] Phone number NOT required")
	return false
}

// ─── HTML extraction helpers ─────────────────────────────────────────────────

var sessionTokenPatterns = []*regexp.Regexp{
	regexp.MustCompile(`<meta\s+name="serialized-sessionToken"\s+content="([^"]+)"`),
	regexp.MustCompile(`<meta\s+name="serialized-session-token"\s+content="([^"]+)"`),
	regexp.MustCompile(`(?i)<meta\s+name="[^"]*session[^"]*token[^"]*"\s+content="([^"]+)"`),
	regexp.MustCompile(`(?i)serialized-sessionToken["'\s]*:\s*["']([^"']+)["']`),
	regexp.MustCompile(`(?i)sessionToken["'\s]*:\s*["']([^"']+)["']`),
}

func extractSessionToken(html string) string {
	for _, re := range sessionTokenPatterns {
		m := re.FindStringSubmatch(html)
		if m != nil && len(m[1]) > 50 {
			token := strings.Trim(m[1], `"'`)
			return htmlUnescape(token)
		}
	}
	return ""
}

var buildIDPatterns = []*regexp.Regexp{
	regexp.MustCompile(`/_next/static/([a-zA-Z0-9_-]{8,64})/_buildManifest\.js`),
	regexp.MustCompile(`"buildId"\s*:\s*"([a-zA-Z0-9_-]{8,64})"`),
	regexp.MustCompile(`/_next/static/([a-zA-Z0-9_-]{8,64})/`),
}

func extractBuildID(html string) string {
	for _, re := range buildIDPatterns {
		m := re.FindStringSubmatch(html)
		if m != nil {
			return m[1]
		}
	}
	return ""
}

func extractFailureCode(receipt map[string]any) string {
	pe := getMap(receipt, "processingError")
	code := getString(pe, "code")
	if code != "" {
		return code
	}
	return "UNKNOWN"
}

// ─── Utility helpers ─────────────────────────────────────────────────────────

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
	// Handle json.Number
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
