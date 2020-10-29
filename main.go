package main

import (
	"bytes"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/go-http-utils/etag"
	"github.com/gomarkdown/markdown"
	"golang.org/x/net/html"
)

func handle(w http.ResponseWriter, r *http.Request) {
	read := func(name string) ([]byte, error) {
		return ioutil.ReadFile(path.Join(os.Args[1], name))
	}

	readExt := func(name string) ([]byte, error) {
		for _, ext := range []string{".md", ".html"} {
			file, err := read(name + ext)
			if err == nil {
				return file, nil
			}
		}
		return read(name)
	}

	if strings.HasPrefix(r.URL.Path, "/.auth/") {
		auth := strings.TrimPrefix(r.URL.Path, "/.auth/")
		http.SetCookie(w, &http.Cookie{Name: "nerka", Value: auth, Path: "/", Secure: true, HttpOnly: true, MaxAge: 31536000})
		w.Header().Set("Location", "..")
		w.WriteHeader(303)
		return
	}

	auth, err := read(".auth")
	if err == nil {
		cookie, err := r.Cookie("nerka")
		if err != nil || cookie.Value != strings.TrimSpace(string(auth)) {
			w.Write([]byte("no"))
			return
		}
	}

	info, err := os.Stat(path.Join(os.Args[1], r.URL.Path))
	if err == nil {
		if info.IsDir() && !strings.HasSuffix(r.URL.Path, "/") {
			w.Header().Set("Location", path.Base(r.URL.Path)+"/")
			w.WriteHeader(303)
			return

		}
		if !info.IsDir() && strings.HasSuffix(r.URL.Path, "/") {
			w.Header().Set("Location", path.Join("..", strings.TrimSuffix(path.Base(r.URL.Path), "/")))
			w.WriteHeader(303)
			return
		}
	}

	header, err := readExt(".header")
	if err == nil {
		w.Write(header)
	}

	var file, md []byte
	var reader io.Reader
	var doc *html.Node
	var f func(*html.Node)

	if strings.HasSuffix(r.URL.Path, "/") {
		file, err = readExt(path.Join(r.URL.Path, "index"))
	} else {
		file, err = readExt(r.URL.Path)
	}

	if err != nil {
		w.Write([]byte(err.Error()))
		goto footer
	}

	if r.URL.Path == "/" {
		w.Write([]byte("<title>nerka!</title>\n"))
	} else {
		w.Write([]byte("<title>nerka: " + strings.TrimPrefix(r.URL.Path, "/") + "</title>\n"))
	}

	md = markdown.ToHTML(file, nil, nil)

	reader = bytes.NewReader(md)
	doc, err = html.Parse(reader)
	if err != nil {
		w.Write([]byte(err.Error()))
		goto footer
	}

	f = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			broken := false
			for _, attr := range n.Attr {
				if attr.Key == "href" {
					if strings.HasPrefix(attr.Val, "https://") ||
						strings.HasPrefix(attr.Val, "//") ||
						strings.HasPrefix(attr.Val, "http://") {
						continue
					}
					_, err := readExt(path.Join(r.URL.Path, attr.Val))
					notFile := err != nil
					_, err = readExt(path.Join(r.URL.Path, attr.Val, "index"))
					notFolder := err != nil
					if notFile && notFolder {
						broken = true
						break
					}
				}
			}
			if broken {
				existingClass := false
				for i := range n.Attr {
					if n.Attr[i].Key == "class" {
						n.Attr[i].Val = n.Attr[i].Val + " broken"
						existingClass = true
						break
					}
				}
				if !existingClass {
					n.Attr = append(n.Attr, html.Attribute{Key: "class", Val: "broken"})
				}
			}

		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			f(c)
		}
	}
	f(doc)
	html.Render(w, doc)

footer:
	footer, err := readExt(".footer")
	if err == nil {
		w.Write(footer)
	}
}

func main() {
	log.Fatal(http.ListenAndServe("127.0.0.1:8002", etag.Handler(http.HandlerFunc(handle), true)))
}
