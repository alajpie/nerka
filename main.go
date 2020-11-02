package main

import (
	"bytes"
	"errors"
	"io/ioutil"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/go-http-utils/etag"
	"github.com/gomarkdown/markdown"
	"github.com/gomarkdown/markdown/parser"
	"golang.org/x/net/html"
)

func read(name string) ([]byte, error) {
	base, err := filepath.Abs(os.Args[1])
	if err != nil {
		return nil, err
	}
	file := path.Join(base, name)
	println(base, file)
	if !strings.HasPrefix(file, base+"/") {
		return nil, errors.New("open " + file + ": directory traversal attack")
	}
	return ioutil.ReadFile(file)
}

func readExt(name string) ([]byte, error) {
	for _, ext := range []string{".md", ".html"} {
		file, err := read(name + ext)
		if err == nil {
			return file, nil
		}
	}
	return read(name)
}

func handle(w http.ResponseWriter, r *http.Request) {
	// set auth cookie
	if strings.HasPrefix(r.URL.Path, "/.auth/") {
		auth := strings.TrimPrefix(r.URL.Path, "/.auth/")
		http.SetCookie(w, &http.Cookie{Name: "nerka", Value: auth, Path: "/", Secure: true, HttpOnly: true, MaxAge: 31536000})
		w.Header().Set("Location", "..")
		w.WriteHeader(303)
		return
	}

	// check auth cookie
	auth, err := read(".auth")
	if err == nil {
		cookie, err := r.Cookie("nerka")
		if err != nil || cookie.Value != strings.TrimSpace(string(auth)) {
			w.Write([]byte("no"))
			return
		}
	}

	// normalize slashes
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

	extension := path.Ext(r.URL.Path)
	if extension != "" && extension != ".md" && extension != ".html" { // static
		file, err := read(r.URL.Path)
		if err != nil {
			w.Write([]byte(err.Error()))
			return
		}
		w.Header().Set("Content-Type", mime.TypeByExtension(extension))
		w.Header().Set("Cache-Control", "max-age=10, stale-while-revalidate=28800")
		w.Write(file)
		return
	}

	// read file or index
	var file []byte
	if strings.HasSuffix(r.URL.Path, "/") {
		file, err = readExt(path.Join(r.URL.Path, "index"))
	} else {
		file, err = readExt(r.URL.Path)
	}
	if err != nil {
		w.Write([]byte(err.Error()))
		return
	}

	// initialize document
	var rawDoc []byte

	// add header
	header, err := readExt(".header")
	if err == nil {
		rawDoc = append(rawDoc, header...)
	}

	// add title
	var title []byte
	if r.URL.Path == "/" {
		title = []byte("<title>nerka!</title>\n")
	} else {
		title = []byte("<title>nerka: " + strings.TrimPrefix(r.URL.Path, "/") + "</title>\n")
	}
	rawDoc = append(rawDoc, title...)

	// add content
	extensions := parser.CommonExtensions | parser.Attributes
	parser := parser.NewWithExtensions(extensions)
	md := markdown.ToHTML(file, parser, nil)
	rawDoc = append(rawDoc, md...)

	// parse HTML
	reader := bytes.NewReader(rawDoc)
	doc, err := html.Parse(reader)
	if err != nil {
		w.Write([]byte(err.Error()))
		return
	}

	// annotate broken links
	var f func(*html.Node)
	f = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			broken := false
			external := false
			for _, attr := range n.Attr {
				if attr.Key == "href" {
					link, err := url.Parse(attr.Val)
					if err != nil {
						broken = true
						break
					}
					if len(link.Host) > 0 {
						external = true
						break
					}
					_, err = readExt(path.Join(path.Dir(r.URL.Path), link.Path))
					notFile := err != nil
					_, err = readExt(path.Join(path.Dir(r.URL.Path), link.Path, "index"))
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
						n.Attr[i].Val = n.Attr[i].Val + " broken-link"
						existingClass = true
						break
					}
				}
				if !existingClass {
					n.Attr = append(n.Attr, html.Attribute{Key: "class", Val: "broken-link"})
				}
			}
			if external {
				existingClass := false
				for i := range n.Attr {
					if n.Attr[i].Key == "class" {
						n.Attr[i].Val = n.Attr[i].Val + " external-link"
						existingClass = true
						break
					}
				}
				if !existingClass {
					n.Attr = append(n.Attr, html.Attribute{Key: "class", Val: "external-link"})
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			f(c)
		}
	}
	f(doc)

	// render HTML
	html.Render(w, doc)
}

func main() {
	log.Fatal(http.ListenAndServe("127.0.0.1:8002", etag.Handler(http.HandlerFunc(handle), true)))
}
