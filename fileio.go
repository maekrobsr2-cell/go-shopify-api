package main

import (
	"bufio"
	"fmt"
	"math/rand/v2"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// ─── Load addresses from file ────────────────────────────────────────────────
// Format: FirstName|LastName|Email|Address|City|State|Zip|Country|Phone|Lat|Long

func loadAddresses(filename string) []Address {
	f, err := os.Open(filename)
	if err != nil {
		return nil
	}
	defer f.Close()

	var addrs []Address
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.Contains(line, "|") {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) < 11 {
			continue
		}

		lat, _ := strconv.ParseFloat(parts[9], 64)
		lon, _ := strconv.ParseFloat(parts[10], 64)
		if lat == 0 {
			lat = 40.7589
		}
		if lon == 0 {
			lon = -73.9851
		}

		addrs = append(addrs, Address{
			FirstName: parts[0],
			LastName:  parts[1],
			Email:     parts[2],
			Address1:  parts[3],
			City:      parts[4],
			Province:  parts[5],
			Zip:       parts[6],
			Country:   parts[7],
			Phone:     cfg.HardcodedPhone,
			Latitude:  lat,
			Longitude: lon,
		})
	}
	if len(addrs) > 0 {
		fmt.Printf("[ADDRESSES] Loaded %d addresses from file\n", len(addrs))
	}
	return addrs
}

func defaultAddress() Address {
	return Address{
		Email:     "test@example.com",
		FirstName: "John",
		LastName:  "Doe",
		Address1:  "4024 College Point Boulevard",
		City:      "Flushing",
		Province:  "NY",
		Zip:       "11354",
		Country:   "US",
		Phone:     cfg.HardcodedPhone,
		Latitude:  40.7589,
		Longitude: -73.9851,
	}
}

func randomAddress(pool []Address) Address {
	if len(pool) == 0 {
		return defaultAddress()
	}
	return pool[rand.IntN(len(pool))]
}

// ─── Load sites from file ────────────────────────────────────────────────────

func loadSites(filename string) []string {
	f, err := os.Open(filename)
	if err != nil {
		fmt.Printf("❌ [ERROR] Sites file not found: %s\n", filename)
		return nil
	}
	defer f.Close()

	seen := make(map[string]bool)
	var sites []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		s := normalizeShopURL(line)
		if s != "" && !seen[s] {
			seen[s] = true
			sites = append(sites, s)
		}
	}
	return sites
}

func normalizeShopURL(u string) string {
	u = strings.TrimSpace(u)
	if u == "" {
		return ""
	}
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		u = "https://" + u
	}
	return u
}

// ─── Load CC file ────────────────────────────────────────────────────────────

func loadCards(filename string) []Card {
	f, err := os.Open(filename)
	if err != nil {
		fmt.Printf("❌ [ERROR] CC file not found: %s\n", filename)
		return nil
	}
	defer f.Close()

	var cards []Card
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		c, ok := parseCCLine(scanner.Text())
		if ok {
			cards = append(cards, c)
		}
	}
	return cards
}

// ─── CC line parser (supports multiple formats) ──────────────────────────────

var (
	reDigitsOnly = regexp.MustCompile(`\D`)
	reCardDigits = regexp.MustCompile(`^\d{13,19}$`)
	// Primary: number|month|year|cvv
	rePrimary = regexp.MustCompile(`(?i)\b(\d{13,19})[\s|/\-.,;:_](0?[1-9]|1[0-2])[\s|/\-.,;:_](\d{4})[\s|/\-.,;:_](\d{3,4})\b`)
	// Short year: number|month|2-digit-year|cvv
	reShortYear = regexp.MustCompile(`(?i)\b(\d{13,19})[\s|/\-.,;:_](0?[1-9]|1[0-2])[\s|/\-.,;:_]('?\d{2})[\s|/\-.,;:_](\d{3,4})\b`)
	// Spaced card: 4111 1111 1111 1111|mm|yyyy|cvv
	reSpaced = regexp.MustCompile(`(?i)\b(\d{4}[\s\-]?\d{4}[\s\-]?\d{4}[\s\-]?\d{3,4})[\s|/\-.,;:_](0?[1-9]|1[0-2])[\s|/\-.,;:_](\d{2,4})[\s|/\-.,;:_](\d{3,4})\b`)
)

func parseCCLine(line string) (Card, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return Card{}, false
	}

	// Try regex patterns in order
	for _, re := range []*regexp.Regexp{rePrimary, reShortYear, reSpaced} {
		m := re.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		number := reDigitsOnly.ReplaceAllString(m[1], "")
		if !reCardDigits.MatchString(number) {
			continue
		}
		month, err := strconv.Atoi(m[2])
		if err != nil || month < 1 || month > 12 {
			continue
		}
		yearStr := strings.TrimLeft(m[3], "'")
		year, err := strconv.Atoi(yearStr)
		if err != nil {
			continue
		}
		if year < 100 {
			year += 2000
		}
		cvv := m[4]
		return Card{
			Number: number,
			Month:  month,
			Year:   year,
			CVV:    cvv,
			Name:   "Test Card",
		}, true
	}

	// Fallback: split by separators
	parts := splitBySeps(line)
	if len(parts) < 4 {
		return Card{}, false
	}
	number := reDigitsOnly.ReplaceAllString(parts[0], "")
	if !reCardDigits.MatchString(number) {
		return Card{}, false
	}
	month, err := strconv.Atoi(parts[1])
	if err != nil || month < 1 || month > 12 {
		return Card{}, false
	}
	year, err := strconv.Atoi(parts[2])
	if err != nil {
		return Card{}, false
	}
	if year < 100 {
		year += 2000
	}
	cvv := parts[3]
	name := "Test Card"
	if len(parts) > 4 {
		name = strings.Join(parts[4:], " ")
	}
	return Card{
		Number: number,
		Month:  month,
		Year:   year,
		CVV:    cvv,
		Name:   name,
	}, true
}

func splitBySeps(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return r == '|' || r == ',' || r == ';' || r == ':' || unicode.IsSpace(r)
	})
}
