package vars

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// DynamicInfo describes a dynamic variable.
type DynamicInfo struct {
	Name    string // with its $
	Desc    string
	Example string
}

type dynamic struct {
	info DynamicInfo
	gen  func(r *Resolver) string
}

var dynamics = map[string]dynamic{}

func def(name, desc, example string, gen func(r *Resolver) string) {
	dynamics[name] = dynamic{DynamicInfo{Name: name, Desc: desc, Example: example}, gen}
}

// Dynamics lists the dynamic variables, sorted by name.
func Dynamics() []DynamicInfo {
	out := make([]DynamicInfo, 0, len(dynamics))
	for _, name := range slices.Sorted(maps.Keys(dynamics)) {
		out = append(out, dynamics[name].info)
	}
	return out
}

func (r *Resolver) intn(n int) int { return r.rng.IntN(n) }

func (r *Resolver) pick(list []string) string { return list[r.intn(len(list))] }

func (r *Resolver) uuid() string {
	var b [16]byte
	for i := range b {
		b[i] = byte(r.rng.Uint32())
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func (r *Resolver) chars(n int, alphabet string) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = alphabet[r.intn(len(alphabet))]
	}
	return string(b)
}

const (
	alnum    = "abcdefghijklmnopqrstuvwxyz0123456789"
	hexChars = "0123456789abcdef"
)

var (
	firstNames = []string{"Ada", "Alan", "Grace", "Linus", "Margaret", "Dennis", "Barbara", "Ken", "Frances", "Donald", "Radia", "Tim", "Hedy", "John", "Katherine", "Guido", "Mei", "Wei", "Yuki", "Priya"}
	lastNames  = []string{"Lovelace", "Turing", "Hopper", "Torvalds", "Hamilton", "Ritchie", "Liskov", "Thompson", "Allen", "Knuth", "Perlman", "Berners-Lee", "Lamarr", "McCarthy", "Johnson", "Rossum", "Chen", "Zhang", "Sato", "Patel"}
	words      = []string{"alpha", "bravo", "cedar", "delta", "ember", "fjord", "garnet", "harbor", "indigo", "juniper", "kelp", "lumen", "maple", "nimbus", "orbit", "pebble", "quartz", "river", "sierra", "tundra", "umber", "velvet", "willow", "xenon", "yarrow", "zephyr"}
	cities     = []string{"Tokyo", "Paris", "Nairobi", "Lima", "Oslo", "Hanoi", "Toronto", "Sydney", "Cairo", "Berlin", "Shanghai", "Austin", "Lisbon", "Seoul", "Dublin"}
	countries  = [][2]string{{"Japan", "JP"}, {"France", "FR"}, {"Kenya", "KE"}, {"Peru", "PE"}, {"Norway", "NO"}, {"Vietnam", "VN"}, {"Canada", "CA"}, {"Australia", "AU"}, {"Egypt", "EG"}, {"Germany", "DE"}, {"China", "CN"}, {"United States", "US"}, {"Portugal", "PT"}, {"South Korea", "KR"}, {"Ireland", "IE"}}
	streets    = []string{"Main St", "Oak Ave", "Elm St", "Maple Rd", "Cedar Ln", "Pine St", "Lake Dr", "Hill Rd"}
	tlds       = []string{"com", "net", "org", "io", "dev", "app"}
	colors     = []string{"red", "orange", "yellow", "green", "blue", "indigo", "violet", "black", "white", "gray", "teal", "magenta"}
	companies  = []string{"Acme", "Globex", "Initech", "Umbrella", "Hooli", "Stark", "Wayne", "Wonka", "Tyrell", "Cyberdyne"}
	suffixes   = []string{"Inc", "LLC", "Group", "Labs", "Systems", "Co"}
	jobs       = []string{"Engineer", "Designer", "Product Manager", "Data Scientist", "Developer Advocate", "Site Reliability Engineer", "Technical Writer", "Researcher"}
	currencies = []string{"USD", "EUR", "JPY", "GBP", "CNY", "AUD", "CAD", "CHF", "KRW", "INR"}
	mimeTypes  = []string{"application/json", "text/plain", "text/html", "image/png", "image/jpeg", "application/pdf", "application/xml", "text/csv"}
	fileExts   = []string{"json", "txt", "html", "png", "jpg", "pdf", "xml", "csv"}
	userAgents = []string{
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36",
		"Mozilla/5.0 (X11; Linux x86_64; rv:127.0) Gecko/20100101 Firefox/127.0",
	}
	lorem = strings.Fields("lorem ipsum dolor sit amet consectetur adipiscing elit sed do eiusmod tempor incididunt ut labore et dolore magna aliqua enim ad minim veniam quis nostrud exercitation ullamco laboris nisi aliquip ex ea commodo consequat")
)

func (r *Resolver) words(n int, list []string) string {
	out := make([]string, n)
	for i := range out {
		out[i] = r.pick(list)
	}
	return strings.Join(out, " ")
}

func (r *Resolver) sentence() string {
	s := r.words(6+r.intn(6), lorem)
	return strings.ToUpper(s[:1]) + s[1:] + "."
}

func (r *Resolver) date(future bool) time.Time {
	d := time.Duration(1+r.intn(365*24)) * time.Hour
	if !future {
		d = -d
	}
	return r.opts.Now().Add(d).UTC().Truncate(time.Second)
}

func init() {
	uuid := func(r *Resolver) string { return r.uuid() }
	def("$uuid", "a random UUID (version 4)", "1b4e28ba-2fa1-41d2-883f-0016d3cca427", uuid)
	def("$guid", "the same as $uuid", "1b4e28ba-2fa1-41d2-883f-0016d3cca427", uuid)
	def("$randomUUID", "the same as $uuid", "1b4e28ba-2fa1-41d2-883f-0016d3cca427", uuid)
	def("$timestamp", "the current time in seconds since 1970", "1793952000", func(r *Resolver) string {
		return fmt.Sprint(r.opts.Now().Unix())
	})
	def("$timestampMs", "the current time in milliseconds since 1970", "1793952000000", func(r *Resolver) string {
		return fmt.Sprint(r.opts.Now().UnixMilli())
	})
	def("$isoTimestamp", "the current time in ISO 8601, UTC", "2026-11-06T08:00:00.000Z", func(r *Resolver) string {
		return r.opts.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	})
	def("$randomInt", "a random integer from 0 to 1000", "472", func(r *Resolver) string { return fmt.Sprint(r.intn(1001)) })
	def("$randomBoolean", "true or false", "true", func(r *Resolver) string { return fmt.Sprint(r.intn(2) == 1) })
	def("$randomFloat", "a random number from 0 to 1000 with two decimals", "381.27", func(r *Resolver) string {
		return fmt.Sprintf("%.2f", float64(r.intn(100001))/100)
	})
	def("$randomPrice", "a random price from 0.00 to 1000.00", "86.45", func(r *Resolver) string {
		return fmt.Sprintf("%d.%02d", r.intn(1000), r.intn(100))
	})
	def("$randomAlphaNumeric", "a random letter or digit", "k", func(r *Resolver) string { return r.chars(1, alnum) })
	def("$randomString", "16 random letters and digits", "x3k9a0pq7m2nb5ty", func(r *Resolver) string { return r.chars(16, alnum) })
	def("$randomHexadecimal", "a random hexadecimal digit", "c", func(r *Resolver) string { return r.chars(1, hexChars) })
	def("$randomFirstName", "a first name", "Grace", func(r *Resolver) string { return r.pick(firstNames) })
	def("$randomLastName", "a last name", "Hopper", func(r *Resolver) string { return r.pick(lastNames) })
	def("$randomFullName", "a first and a last name", "Grace Hopper", func(r *Resolver) string {
		return r.pick(firstNames) + " " + r.pick(lastNames)
	})
	def("$randomUserName", "a user name", "grace.hopper42", func(r *Resolver) string {
		return strings.ToLower(r.pick(firstNames)+"."+r.pick(lastNames)) + fmt.Sprint(r.intn(100))
	})
	def("$randomEmail", "an email address at an example domain", "grace.hopper@example.com", func(r *Resolver) string {
		return strings.ToLower(r.pick(firstNames)+"."+strings.ReplaceAll(r.pick(lastNames), "-", "")) + "@example." + r.pick([]string{"com", "net", "org"})
	})
	def("$randomPassword", "15 random letters and digits", "a8k2m9x0q4r7t1z", func(r *Resolver) string { return r.chars(15, alnum) })
	def("$randomPhoneNumber", "a phone number", "555-012-3456", func(r *Resolver) string {
		return fmt.Sprintf("555-%03d-%04d", r.intn(1000), r.intn(10000))
	})
	def("$randomJobTitle", "a job title", "Technical Writer", func(r *Resolver) string { return r.pick(jobs) })
	def("$randomCompanyName", "a company's name", "Initech Labs", func(r *Resolver) string {
		return r.pick(companies) + " " + r.pick(suffixes)
	})
	def("$randomCity", "a city", "Lisbon", func(r *Resolver) string { return r.pick(cities) })
	def("$randomCountry", "a country", "Portugal", func(r *Resolver) string { return countries[r.intn(len(countries))][0] })
	def("$randomCountryCode", "an ISO 3166 country code", "PT", func(r *Resolver) string { return countries[r.intn(len(countries))][1] })
	def("$randomStreetAddress", "a street address", "221 Oak Ave", func(r *Resolver) string {
		return fmt.Sprintf("%d %s", 1+r.intn(9999), r.pick(streets))
	})
	def("$randomLatitude", "a latitude", "38.7223", func(r *Resolver) string {
		return fmt.Sprintf("%.4f", float64(r.intn(1800001))/10000-90)
	})
	def("$randomLongitude", "a longitude", "-9.1393", func(r *Resolver) string {
		return fmt.Sprintf("%.4f", float64(r.intn(3600001))/10000-180)
	})
	def("$randomIP", "an IPv4 address", "192.168.12.34", func(r *Resolver) string {
		return fmt.Sprintf("%d.%d.%d.%d", 1+r.intn(254), r.intn(256), r.intn(256), 1+r.intn(254))
	})
	def("$randomIPV6", "an IPv6 address", "2001:db8:85a3:0:0:8a2e:370:7334", func(r *Resolver) string {
		parts := make([]string, 8)
		for i := range parts {
			parts[i] = fmt.Sprintf("%x", r.intn(0x10000))
		}
		return strings.Join(parts, ":")
	})
	def("$randomMACAddress", "a MAC address", "3c:22:fb:01:9a:7e", func(r *Resolver) string {
		parts := make([]string, 6)
		for i := range parts {
			parts[i] = r.chars(2, hexChars)
		}
		return strings.Join(parts, ":")
	})
	def("$randomDomainName", "a domain name", "harbor-quartz.io", func(r *Resolver) string {
		return r.pick(words) + "-" + r.pick(words) + "." + r.pick(tlds)
	})
	def("$randomUrl", "an https URL", "https://harbor-quartz.io", func(r *Resolver) string {
		return "https://" + r.pick(words) + "-" + r.pick(words) + "." + r.pick(tlds)
	})
	def("$randomUserAgent", "a browser's User-Agent", userAgents[0], func(r *Resolver) string { return r.pick(userAgents) })
	def("$randomSemver", "a semantic version", "2.14.3", func(r *Resolver) string {
		return fmt.Sprintf("%d.%d.%d", r.intn(10), r.intn(20), r.intn(30))
	})
	def("$randomColor", "a color's name", "teal", func(r *Resolver) string { return r.pick(colors) })
	def("$randomHexColor", "a color in hex", "#1a9f6c", func(r *Resolver) string { return "#" + r.chars(6, hexChars) })
	def("$randomWord", "a word", "juniper", func(r *Resolver) string { return r.pick(words) })
	def("$randomWords", "three words", "maple orbit kelp", func(r *Resolver) string { return r.words(3, words) })
	def("$randomLoremWord", "a lorem ipsum word", "tempor", func(r *Resolver) string { return r.pick(lorem) })
	def("$randomLoremSentence", "a lorem ipsum sentence", "Dolor sit amet elit sed.", func(r *Resolver) string { return r.sentence() })
	def("$randomLoremParagraph", "a lorem ipsum paragraph", "Lorem ipsum dolor. Sed do eiusmod.", func(r *Resolver) string {
		s := make([]string, 3+r.intn(3))
		for i := range s {
			s[i] = r.sentence()
		}
		return strings.Join(s, " ")
	})
	def("$randomCurrencyCode", "an ISO 4217 currency code", "EUR", func(r *Resolver) string { return r.pick(currencies) })
	def("$randomMimeType", "a MIME type", "application/json", func(r *Resolver) string { return r.pick(mimeTypes) })
	def("$randomFileName", "a file name", "cedar.json", func(r *Resolver) string { return r.pick(words) + "." + r.pick(fileExts) })
	def("$randomDateFuture", "a time within the coming year, ISO 8601", "2027-03-14T09:00:00Z", func(r *Resolver) string {
		return r.date(true).Format(time.RFC3339)
	})
	def("$randomDatePast", "a time within the past year, ISO 8601", "2026-02-01T17:00:00Z", func(r *Resolver) string {
		return r.date(false).Format(time.RFC3339)
	})
	def("$randomDateRecent", "a time within the past day, ISO 8601", "2026-11-05T22:10:00Z", func(r *Resolver) string {
		return r.opts.Now().Add(-time.Duration(1+r.intn(86400)) * time.Second).UTC().Format(time.RFC3339)
	})
}
