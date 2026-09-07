package source

import (
	"bytes"
	"encoding/xml"
	"strings"
	"time"
)

// The news and image namespaces appear inline in the struct tags below,
// because encoding/xml needs them there as literals.

// urlEntry is one <url> record in a sitemap.
type urlEntry struct {
	Loc     string       `xml:"loc"`
	LastMod string       `xml:"lastmod"`
	News    newsDetail   `xml:"http://www.google.com/schemas/sitemap-news/0.9 news"`
	Images  []imageEntry `xml:"http://www.google.com/schemas/sitemap-image/1.1 image"`
}

// newsDetail is the news: extension block. It is optional: publishers mix
// annotated and plain <url> records under one index.
type newsDetail struct {
	PublicationDate string `xml:"http://www.google.com/schemas/sitemap-news/0.9 publication_date"`
	Keywords        string `xml:"http://www.google.com/schemas/sitemap-news/0.9 keywords"`
	Title           string `xml:"http://www.google.com/schemas/sitemap-news/0.9 title"`
}

type imageEntry struct {
	Loc string `xml:"http://www.google.com/schemas/sitemap-image/1.1 loc"`
}

// indexEntry is one <sitemap> record in a sitemap index. LastMod is kept
// because it lets a later change skip children that have not moved since
// the previous crawl — Times of India's index carries one timestamp per
// child, and only one of them typically changes between ticks.
type indexEntry struct {
	Loc     string
	LastMod time.Time
}

type rawIndexEntry struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod"`
}

// newDecoder returns a decoder tolerant of the malformed character
// entities some publisher sitemaps emit. With Strict false an unknown
// entity such as a bare "&K;" stays literal text rather than failing the
// document, so one bad entity no longer drops a whole source's crawl.
func newDecoder(data []byte) *xml.Decoder {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	return dec
}

// documentKind reports whether a document is a sitemap, a sitemap index, or
// neither. Without this check, encoding/xml happily decodes an HTML error
// page or an RSS/Atom feed into a struct with no matching children, which
// looks identical to a genuinely empty sitemap: zero articles, no error.
type documentKind int

// The recognised document kinds. kindUnknown is the zero value so a
// caller that forgets to check an error still gets the safe outcome.
const (
	kindUnknown documentKind = iota
	kindURLSet
	kindIndex
)

// rootKind reads the root element of an XML document and reports which
// kind of sitemap document it is, along with the root element's local
// name so a caller can name it in an error. It reads only as far as the
// first start element, so malformed markup deeper in the document (for
// example the entities newDecoder tolerates) never reaches this check.
func rootKind(data []byte) (documentKind, string, error) {
	dec := newDecoder(data)
	for {
		tok, err := dec.Token()
		if err != nil {
			return kindUnknown, "", err
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "urlset":
			return kindURLSet, se.Name.Local, nil
		case "sitemapindex":
			return kindIndex, se.Name.Local, nil
		default:
			return kindUnknown, se.Name.Local, nil
		}
	}
}

// parseURLSet decodes the <url> records from a sitemap.
func parseURLSet(data []byte) ([]urlEntry, error) {
	var doc struct {
		URLs []urlEntry `xml:"url"`
	}
	if err := newDecoder(data).Decode(&doc); err != nil {
		return nil, err
	}
	return doc.URLs, nil
}

// parseSitemapIndex decodes the child sitemaps from an index, dropping
// entries with a blank location.
func parseSitemapIndex(data []byte) ([]indexEntry, error) {
	var doc struct {
		Sitemaps []rawIndexEntry `xml:"sitemap"`
	}
	if err := newDecoder(data).Decode(&doc); err != nil {
		return nil, err
	}

	out := make([]indexEntry, 0, len(doc.Sitemaps))
	for _, s := range doc.Sitemaps {
		loc := strings.TrimSpace(s.Loc)
		if loc == "" {
			continue
		}
		entry := indexEntry{Loc: loc}
		if t, ok := parsePublicationDate(s.LastMod); ok {
			entry.LastMod = t
		}
		out = append(out, entry)
	}
	return out, nil
}

// publicationLayouts are tried in order. The old code accepted RFC3339
// alone and silently returned the zero time for everything else, which put
// those articles at the Unix epoch for any consumer sorting by date.
var publicationLayouts = []string{
	time.RFC3339,
	"2006-01-02T15:04:05Z0700",
	time.RFC1123Z,
	time.RFC1123,
	"2006-01-02 15:04:05",
	"2006-01-02",
}

// parsePublicationDate parses a sitemap timestamp, reporting whether it
// succeeded so the caller can distinguish "no date" from "the epoch".
func parsePublicationDate(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	for _, layout := range publicationLayouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// parseKeywords splits the comma-separated news:keywords value.
func parseKeywords(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if kw := strings.TrimSpace(p); kw != "" {
			out = append(out, kw)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// firstImage returns the first non-blank image location.
func firstImage(images []imageEntry) string {
	for _, img := range images {
		if loc := strings.TrimSpace(img.Loc); loc != "" {
			return loc
		}
	}
	return ""
}
