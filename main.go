package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io/ioutil"
	"log"
	"mime"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"

	"golang.org/x/net/html"

	"github.com/PuerkitoBio/goquery"
)

var bind = flag.String("bind", ":8080", "the address to bind to")
var templates = template.Must(template.ParseGlob("templates/*"))

func main() {
	http.HandleFunc("/view/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")

		urlPrefix := strings.Join(parts[:4], "/") + "/"

		var newURL url.URL
		newURL.Scheme = parts[2]
		newURL.Host = parts[3]
		newURL.Path = "/" + strings.Join(parts[4:], "/")
		newURL.RawQuery = r.URL.RawQuery

		log.Printf("Proxying %q", newURL.String())

		r.Host = newURL.Host
		r.Header.Set("Host", newURL.Host)
		r.URL = &newURL
		r.RequestURI = ""
		r.Header.Del("Accept-Encoding")
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		defer resp.Body.Close()
		buf, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		contentType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
		if err != nil {
			contentType = http.DetectContentType(buf)
		}

		log.Printf("Content-Type: %s", contentType)

		if contentType == "text/html" {
			rules := getRules(r)

			var regexps []*regexp.Regexp
			for _, rule := range rules {
				r, err := regexp.Compile(rule.Match)
				if err != nil {
					log.Println(err)
				}
				regexps = append(regexps, r)
			}

			doc, err := goquery.NewDocumentFromReader(bytes.NewReader(buf))
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}

			doc.Find("a, link").Each(func(_ int, s *goquery.Selection) {
				rewriteAttr(s, "href", urlPrefix)
			})
			doc.Find("img, script").Each(func(_ int, s *goquery.Selection) {
				rewriteAttr(s, "src", urlPrefix)
			})
			doc.Find("form").Each(func(_ int, s *goquery.Selection) {
				rewriteAttr(s, "action", urlPrefix)
			})

			for _, n := range doc.Selection.Nodes {
				Walk(n, func(n *html.Node) {
					if n.Type == html.TextNode {
						for i, r := range regexps {
							n.Data = r.ReplaceAllString(n.Data, rules[i].Replace)
						}
					}
				})
			}

			body, err := goquery.OuterHtml(doc.Selection)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			buf = []byte(body)
		}

		resp.Header.Del("Content-Security-Policy")

		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		w.Write(buf)
	})
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		rules := getRules(r)
		if r.Method == "POST" {
			match := r.FormValue("match")
			replace := r.FormValue("replace")

			_, err := regexp.Compile(match)
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}

			rules = append(rules, Rule{match, replace})

			cookieBody, _ := json.Marshal(rules)
			http.SetCookie(w, &http.Cookie{
				Name:     "rules",
				Value:    base64.StdEncoding.EncodeToString(cookieBody),
				Path:     "/",
				HttpOnly: true,
			})
		}
		templates.ExecuteTemplate(w, "index.html", rules)
	})

	log.Printf("Listening on %s...", *bind)
	log.Fatal(http.ListenAndServe(*bind, nil))
}

// Walk calls f once for the node and each of it's descendants.
func Walk(n *html.Node, f func(*html.Node)) {
	f(n)
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		Walk(child, f)
	}
}

func getRules(r *http.Request) []Rule {
	var rules []Rule
	rulesCookie, err := r.Cookie("rules")
	if err != nil {
		return nil
	}
	cookieBody, err := base64.StdEncoding.DecodeString(rulesCookie.Value)
	if err != nil {
		return nil
	}
	if err := json.Unmarshal(cookieBody, &rules); err != nil {
		return nil
	}
	return rules
}

// Rule represents a single match replace rule.
type Rule struct {
	Match, Replace string
}

func rewriteAttr(s *goquery.Selection, attr, urlPrefix string) {
	href := s.AttrOr(attr, "")
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") || strings.HasPrefix(href, "//") {
		parsed, err := url.Parse(href)
		if len(parsed.Scheme) == 0 {
			parsed.Scheme = "https"
		}
		if err == nil {
			href = fmt.Sprintf("/view/%s/%s%s", parsed.Scheme, parsed.Host, parsed.Path)
			if len(parsed.RawQuery) > 0 {
				href += "?" + parsed.RawQuery
			}
			if len(parsed.Fragment) > 0 {
				href += "#" + parsed.Fragment
			}
		}
	} else if strings.HasPrefix(href, "/") {
		href = path.Join(urlPrefix, href)
	}
	if len(href) > 0 {
		s.SetAttr(attr, href)
	}
}
