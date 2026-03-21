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

// ─── Name/email pools for auto-generation ────────────────────────────────────

var firstNames = []string{
	"James", "Mary", "Robert", "Patricia", "John", "Jennifer", "Michael", "Linda",
	"David", "Elizabeth", "William", "Barbara", "Richard", "Susan", "Joseph", "Jessica",
	"Thomas", "Sarah", "Christopher", "Karen", "Charles", "Lisa", "Daniel", "Nancy",
	"Matthew", "Betty", "Anthony", "Margaret", "Mark", "Sandra", "Donald", "Ashley",
	"Steven", "Kimberly", "Andrew", "Emily", "Paul", "Donna", "Joshua", "Michelle",
	"Kenneth", "Carol", "Kevin", "Amanda", "Brian", "Dorothy", "George", "Melissa",
	"Timothy", "Deborah", "Ronald", "Stephanie", "Edward", "Rebecca", "Jason", "Sharon",
	"Jeffrey", "Laura", "Ryan", "Cynthia", "Jacob", "Kathleen", "Gary", "Amy",
}

var lastNames = []string{
	"Smith", "Johnson", "Williams", "Brown", "Jones", "Garcia", "Miller", "Davis",
	"Rodriguez", "Martinez", "Hernandez", "Lopez", "Gonzalez", "Wilson", "Anderson",
	"Thomas", "Taylor", "Moore", "Jackson", "Martin", "Lee", "Perez", "Thompson",
	"White", "Harris", "Sanchez", "Clark", "Ramirez", "Lewis", "Robinson", "Walker",
	"Young", "Allen", "King", "Wright", "Scott", "Torres", "Nguyen", "Hill", "Flores",
	"Green", "Adams", "Nelson", "Baker", "Hall", "Rivera", "Campbell", "Mitchell",
	"Carter", "Roberts", "Gomez", "Phillips", "Evans", "Turner", "Diaz", "Parker",
}

var emailDomains = []string{
	"gmail.com", "yahoo.com", "outlook.com", "hotmail.com", "icloud.com",
	"aol.com", "protonmail.com", "mail.com", "live.com", "msn.com",
}

// US state abbreviations → approximate lat/lon center
var stateCoords = map[string][2]float64{
	"AL": {32.806671, -86.791130}, "AK": {61.370716, -152.404419},
	"AZ": {33.729759, -111.431221}, "AR": {34.969704, -92.373123},
	"CA": {36.116203, -119.681564}, "CO": {39.059811, -105.311104},
	"CT": {41.597782, -72.755371}, "DE": {39.318523, -75.507141},
	"FL": {27.766279, -81.686783}, "GA": {33.040619, -83.643074},
	"HI": {21.094318, -157.498337}, "ID": {44.240459, -114.478773},
	"IL": {40.349457, -88.986137}, "IN": {39.849426, -86.258278},
	"IA": {42.011539, -93.210526}, "KS": {38.526600, -96.726486},
	"KY": {37.668140, -84.670067}, "LA": {31.169546, -91.867805},
	"ME": {44.693947, -69.381927}, "MD": {39.063946, -76.802101},
	"MA": {42.230171, -71.530106}, "MI": {43.326618, -84.536095},
	"MN": {45.694454, -93.900192}, "MS": {32.741646, -89.678696},
	"MO": {38.456085, -92.288368}, "MT": {46.921925, -110.454353},
	"NE": {41.125370, -98.268082}, "NV": {38.313515, -117.055374},
	"NH": {43.452492, -71.563896}, "NJ": {40.298904, -74.521011},
	"NM": {34.840515, -106.248482}, "NY": {42.165726, -74.948051},
	"NC": {35.630066, -79.806419}, "ND": {47.528912, -99.784012},
	"OH": {40.388783, -82.764915}, "OK": {35.565342, -96.928917},
	"OR": {44.572021, -122.070938}, "PA": {40.590752, -77.209755},
	"RI": {41.680893, -71.511780}, "SC": {33.856892, -80.945007},
	"SD": {44.299782, -99.438828}, "TN": {35.747845, -86.692345},
	"TX": {31.054487, -97.563461}, "UT": {40.150032, -111.862434},
	"VT": {44.045876, -72.710686}, "VA": {37.769337, -78.169968},
	"WA": {47.400902, -121.490494}, "WV": {38.491226, -80.954453},
	"WI": {44.268543, -89.616508}, "WY": {42.755966, -107.302490},
	"DC": {38.897438, -77.026817},
}

func randFirstName() string { return firstNames[rand.IntN(len(firstNames))] }
func randLastName() string  { return lastNames[rand.IntN(len(lastNames))] }
func randEmail(first, last string) string {
	domain := emailDomains[rand.IntN(len(emailDomains))]
	num := rand.IntN(9999)
	return fmt.Sprintf("%s.%s%d@%s", strings.ToLower(first), strings.ToLower(last), num, domain)
}
func randPhone() string {
	// Generate a random US phone number (area code 200-999)
	area := 200 + rand.IntN(800)
	mid := 200 + rand.IntN(800)
	end := rand.IntN(10000)
	return fmt.Sprintf("%03d%03d%04d", area, mid, end)
}

// ─── Load addresses from file ────────────────────────────────────────────────
// Supports two formats:
//   1. Pipe-delimited: FirstName|LastName|Email|Address|City|State|Zip|Country|Phone|Lat|Long
//   2. Simple: "777 Brockton Avenue, Abington MA 2351" (auto-generates names/email/phone)

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
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Try pipe-delimited format first
		if strings.Contains(line, "|") {
			parts := strings.Split(line, "|")
			if len(parts) >= 11 {
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
				continue
			}
		}

		// Simple format: "777 Brockton Avenue, Abington MA 2351"
		// or: "777 Brockton Avenue, Abington MA 02351"
		addr := parseSimpleAddress(line)
		if addr != nil {
			addrs = append(addrs, *addr)
		}
	}
	if len(addrs) > 0 {
		fmt.Printf("[ADDRESSES] Loaded %d addresses from file\n", len(addrs))
	}
	return addrs
}

// parseSimpleAddress parses "777 Brockton Avenue, Abington MA 2351" format
// and auto-generates first/last name, email, phone, lat/long
func parseSimpleAddress(line string) *Address {
	// Split on comma: "777 Brockton Avenue" , " Abington MA 2351"
	commaIdx := strings.LastIndex(line, ",")
	if commaIdx < 0 {
		return nil
	}

	streetPart := strings.TrimSpace(line[:commaIdx])
	remainder := strings.TrimSpace(line[commaIdx+1:])

	if streetPart == "" || remainder == "" {
		return nil
	}

	// Parse remainder: "Abington MA 2351" → city, state, zip
	// Work backwards: zip is last token, state is second-to-last, city is everything before
	tokens := strings.Fields(remainder)
	if len(tokens) < 3 {
		return nil
	}

	zip := tokens[len(tokens)-1]
	state := strings.ToUpper(tokens[len(tokens)-2])
	city := strings.Join(tokens[:len(tokens)-2], " ")

	// Pad zip to 5 digits if needed (e.g. "2351" → "02351")
	for len(zip) < 5 {
		zip = "0" + zip
	}

	// Validate state is a known US state abbreviation
	coords, known := stateCoords[state]
	lat, lon := 40.7589, -73.9851 // default NYC
	if known {
		// Add small jitter to lat/lon so they're not all identical
		lat = coords[0] + (rand.Float64()-0.5)*0.1
		lon = coords[1] + (rand.Float64()-0.5)*0.1
	}

	first := randFirstName()
	last := randLastName()

	return &Address{
		Email:     randEmail(first, last),
		FirstName: first,
		LastName:  last,
		Address1:  streetPart,
		City:      city,
		Province:  state,
		Zip:       zip,
		Country:   "US",
		Phone:     randPhone(),
		Latitude:  lat,
		Longitude: lon,
	}
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
