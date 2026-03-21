package main

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"sync"
)

// ─── Card ────────────────────────────────────────────────────────────────────

type Card struct {
	Number string `json:"number"`
	Month  int    `json:"month"`
	Year   int    `json:"year"`
	CVV    string `json:"verification_value"`
	Name   string `json:"name"`
}

func (c Card) Masked() string {
	d := onlyDigits(c.Number)
	if len(d) >= 4 {
		return "**** **** **** " + d[len(d)-4:]
	}
	return "****"
}

func (c Card) Formatted() string {
	return fmt.Sprintf("%s|%02d|%02d|%s", c.Number, c.Month, c.Year%100, c.CVV)
}

// ─── Address ─────────────────────────────────────────────────────────────────

type Address struct {
	Email     string  `json:"email"`
	FirstName string  `json:"first_name"`
	LastName  string  `json:"last_name"`
	Address1  string  `json:"address1"`
	City      string  `json:"city"`
	Province  string  `json:"province"`
	Zip       string  `json:"zip"`
	Country   string  `json:"country"`
	Phone     string  `json:"phone"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

// ─── Browser fingerprint ─────────────────────────────────────────────────────

type Fingerprint struct {
	Impersonate     string // e.g. "chrome131"
	UserAgent       string
	SecCHUA         string
	SecCHUAPlatform string
	SecCHUAMobile   string
}

// ─── Checkout session — carries ALL state for one checkout attempt ────────────

type CheckoutSession struct {
	Client    *http.Client
	ProxyURL  string // needed for Step2 tokenization (separate client)
	ShopURL   string
	VariantID string
	Card      Card
	Addr      Address
	FP        Fingerprint
	BuildID   string // checkout-web build hash

	// populated during flow
	CheckoutToken  string
	SessionToken   string
	MerchandiseID  string
	QueueToken     string
	ShippingHandle string
	ShippingAmount string
	ActualTotal    string
	DeliveryExps   []map[string]string // [{signedHandle: "..."}]
	PhoneRequired  bool
	CardSessionID  string
}

// ─── Result of a single card attempt ─────────────────────────────────────────

type Result struct {
	Card        Card
	Code        string
	Amount      string
	Elapsed     float64
	Site        string
	Success     bool
	RawResponse map[string]any
}

// ─── Product info ────────────────────────────────────────────────────────────

type Product struct {
	ID        string
	VariantID string
	Price     float64
	PriceStr  string
	Title     string
}

// ─── Thread-safe product cache ───────────────────────────────────────────────

type ProductCache struct {
	mu    sync.RWMutex
	cache map[string]*Product // keyed by shop URL
}

func NewProductCache() *ProductCache {
	return &ProductCache{cache: make(map[string]*Product)}
}

func (pc *ProductCache) Get(shopURL string) (*Product, bool) {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	p, ok := pc.cache[shopURL]
	return p, ok
}

func (pc *ProductCache) Set(shopURL string, p *Product) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.cache[shopURL] = p
}

// ─── GraphQL query constants & APQ hashes ────────────────────────────────────

const ProposalFullQuery = `query Proposal($delivery:DeliveryTermsInput,$discounts:DiscountTermsInput,$payment:PaymentTermInput,$merchandise:MerchandiseTermInput,$buyerIdentity:BuyerIdentityTermInput,$taxes:TaxTermInput,$sessionInput:SessionTokenInput!,$tip:TipTermInput,$note:NoteInput,$scriptFingerprint:ScriptFingerprintInput,$optionalDuties:OptionalDutiesInput,$cartMetafields:[CartMetafieldOperationInput!],$memberships:MembershipsInput){session(sessionInput:$sessionInput){negotiate(input:{purchaseProposal:{delivery:$delivery,discounts:$discounts,payment:$payment,merchandise:$merchandise,buyerIdentity:$buyerIdentity,taxes:$taxes,tip:$tip,note:$note,scriptFingerprint:$scriptFingerprint,optionalDuties:$optionalDuties,cartMetafields:$cartMetafields,memberships:$memberships}}){__typename result{...on NegotiationResultAvailable{queueToken sellerProposal{deliveryExpectations{...on FilledDeliveryExpectationTerms{deliveryExpectations{signedHandle __typename}__typename}...on PendingTerms{pollDelay __typename}__typename}delivery{...on FilledDeliveryTerms{deliveryLines{availableDeliveryStrategies{...on CompleteDeliveryStrategy{handle phoneRequired amount{...on MoneyValueConstraint{value{amount currencyCode __typename}__typename}__typename}__typename}__typename}__typename}__typename}...on PendingTerms{pollDelay __typename}__typename}checkoutTotal{...on MoneyValueConstraint{value{amount currencyCode __typename}__typename}__typename}__typename}__typename}__typename}}}}` //nolint:lll

const ProposalPollQuery = `query Proposal($delivery:DeliveryTermsInput,$discounts:DiscountTermsInput,$payment:PaymentTermInput,$merchandise:MerchandiseTermInput,$buyerIdentity:BuyerIdentityTermInput,$taxes:TaxTermInput,$sessionInput:SessionTokenInput!,$tip:TipTermInput,$note:NoteInput,$scriptFingerprint:ScriptFingerprintInput,$optionalDuties:OptionalDutiesInput,$cartMetafields:[CartMetafieldOperationInput!],$memberships:MembershipsInput){session(sessionInput:$sessionInput){negotiate(input:{purchaseProposal:{delivery:$delivery,discounts:$discounts,payment:$payment,merchandise:$merchandise,buyerIdentity:$buyerIdentity,taxes:$taxes,tip:$tip,note:$note,scriptFingerprint:$scriptFingerprint,optionalDuties:$optionalDuties,cartMetafields:$cartMetafields,memberships:$memberships}}){__typename result{...on NegotiationResultAvailable{queueToken sellerProposal{deliveryExpectations{...on FilledDeliveryExpectationTerms{deliveryExpectations{signedHandle __typename}__typename}...on PendingTerms{pollDelay __typename}__typename}__typename}__typename}__typename}}}}` //nolint:lll

const SubmitCompletionQuery = `mutation SubmitForCompletion($input:NegotiationInput!,$attemptToken:String!,$metafields:[MetafieldInput!],$postPurchaseInquiryResult:PostPurchaseInquiryResultCode,$analytics:AnalyticsInput){submitForCompletion(input:$input attemptToken:$attemptToken metafields:$metafields postPurchaseInquiryResult:$postPurchaseInquiryResult analytics:$analytics){...on SubmitSuccess{receipt{...on ProcessedReceipt{id __typename}...on ProcessingReceipt{id __typename}__typename}__typename}...on SubmitAlreadyAccepted{receipt{...on ProcessedReceipt{id __typename}...on ProcessingReceipt{id __typename}__typename}__typename}...on SubmitFailed{reason __typename}...on SubmitRejected{errors{__typename code localizedMessage}__typename}...on Throttled{pollAfter __typename}...on SubmittedForCompletion{receipt{...on ProcessedReceipt{id __typename}...on ProcessingReceipt{id __typename}__typename}__typename}__typename}}` //nolint:lll

const PollReceiptQuery = `query PollForReceipt($receiptId:ID!,$sessionToken:String!){receipt(receiptId:$receiptId,sessionInput:{sessionToken:$sessionToken}){...on ProcessedReceipt{id __typename}...on ProcessingReceipt{id pollDelay __typename}...on FailedReceipt{id processingError{...on PaymentFailed{code messageUntranslated hasOffsitePaymentMethod __typename}...on InventoryClaimFailure{__typename}...on InventoryReservationFailure{__typename}...on OrderCreationFailure{paymentsHaveBeenReverted __typename}...on OrderCreationSchedulingFailure{__typename}...on DiscountUsageLimitExceededFailure{__typename}...on CustomerPersistenceFailure{__typename}__typename}__typename}...on ActionRequiredReceipt{id __typename}__typename}}` //nolint:lll

// Pre-computed SHA-256 hashes for Apollo APQ protocol
var QueryHashes = map[string]string{
	ProposalFullQuery:     sha256Hex(ProposalFullQuery),
	ProposalPollQuery:     sha256Hex(ProposalPollQuery),
	SubmitCompletionQuery: sha256Hex(SubmitCompletionQuery),
	PollReceiptQuery:      sha256Hex(PollReceiptQuery),
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h)
}

// addAPQExtensions adds Apollo Automatic Persisted Query extensions to a GraphQL payload
func addAPQExtensions(payload map[string]any) map[string]any {
	q, ok := payload["query"].(string)
	if !ok {
		return payload
	}
	hash, exists := QueryHashes[q]
	if !exists {
		return payload
	}
	payload["extensions"] = map[string]any{
		"persistedQuery": map[string]any{
			"version":    1,
			"sha256Hash": hash,
		},
	}
	return payload
}
