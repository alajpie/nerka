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
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-http-utils/etag"
	"github.com/gomarkdown/markdown"
	"github.com/gomarkdown/markdown/parser"
	"github.com/tdewolff/minify/v2"
	"github.com/tdewolff/minify/v2/css"
	mhtml "github.com/tdewolff/minify/v2/html"
	"github.com/tdewolff/minify/v2/js"
	"golang.org/x/net/html"
)

func read(name string) ([]byte, error) {
	base, err := filepath.Abs(os.Args[1])
	if err != nil {
		return nil, err
	}
	file := path.Join(base, name)
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

var mutex sync.Mutex
var locks map[string][]byte
var lockExpires map[string]time.Time

func lockOrExtend(page string, lock []byte) error {
	mutex.Lock()
	defer mutex.Unlock()

	currentLock, hasLock := locks[page]
	if hasLock && bytes.Compare(currentLock, lock) != 0 {
		return errors.New("already locked")
	}

	// lock
	expires := time.Now().Add(time.Minute)
	locks[page] = lock
	lockExpires[page] = expires

	go func(page string) {
		for {
			mutex.Lock()
			expires = lockExpires[page]
			// lock expired
			if expires.Before(time.Now()) {
				delete(locks, page)
				delete(lockExpires, page)
				mutex.Unlock()
				return
			}
			mutex.Unlock()
			time.Sleep(expires.Sub(time.Now()))
		}
	}(page)
	return nil
}

func unlock(page string, lock []byte) error {
	mutex.Lock()
	defer mutex.Unlock()

	currentLock, hasLock := locks[page]
	if hasLock && bytes.Compare(currentLock, lock) != 0 {
		return errors.New("locked by someone else")
	}

	delete(locks, page)
	delete(lockExpires, page)
	return nil
}

func handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Vary", "Cookie")
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
			w.WriteHeader(403)
			w.Header().Set("Cache-Control", "max-age=604800, immutable")
			w.Write([]byte("no"))
			return
		}
	}

	// handle locks
	if strings.HasSuffix(r.URL.Path, "/.lock") {
		if r.Method != "POST" {
			w.Header().Set("Allow", "POST")
			w.WriteHeader(405)
			w.Write([]byte("only POSTing locks is allowed"))
			return
		}
		page := strings.TrimSuffix(r.URL.Path, "/.lock")
		if _, err := readExt(page); err != nil {
			w.WriteHeader(404)
			w.Write([]byte(err.Error()))
			return
		}

		buf := new(bytes.Buffer)
		buf.ReadFrom(r.Body)
		lock := buf.Bytes()
		if len(lock) == 0 {
			w.WriteHeader(400)
			w.Write([]byte("lock can't be empty"))
			return
		}

		err = lockOrExtend(page, lock)
		if err != nil {
			w.WriteHeader(409)
			w.Write([]byte(err.Error()))
			return
		}
		w.WriteHeader(200)
		return
	}

	if r.Method != "GET" && r.Method != "POST" {
		w.Header().Set("Allow", "GET, POST")
		w.WriteHeader(405)
		w.Write([]byte("only GETting or POSTing pages is allowed"))
		return
	}

	// normalize slashes
	info, err := os.Stat(path.Join(os.Args[1], r.URL.Path))
	if err == nil {
		w.Header().Set("Cache-Control", "max-age=604800")
		if info.IsDir() && !strings.HasSuffix(r.URL.Path, "/") {
			w.Header().Set("Location", path.Base(r.URL.Path)+"/")
			w.WriteHeader(303)
			return
		}
		if !info.IsDir() && strings.HasSuffix(r.URL.Path, "/") {
			w.Header().Set("Location", path.Join("..", path.Base(r.URL.Path), "/"))
			w.WriteHeader(303)
			return
		}
	}

	m := minify.New()
	m.AddFunc("text/html", mhtml.Minify)
	m.AddFunc("text/css", css.Minify)
	m.AddFuncRegexp(regexp.MustCompile("^(application|text)/(x-)?(java|ecma)script$"), js.Minify)

	extension := path.Ext(r.URL.Path)
	if extension != "" && extension != ".md" && extension != ".html" { // static
		file, err := read(r.URL.Path)
		if err != nil {
			w.Write([]byte(err.Error()))
			return
		}
		w.Header().Set("Cache-Control", "max-age=300, stale-while-revalidate=28800")
		w.Header().Set("Content-Type", mime.TypeByExtension(extension))
		b, err := m.Bytes(mime.TypeByExtension(extension), file)
		if err != nil {
			w.Write(file)
			return
		}
		w.Write(b)
		return
	}

	w.Header().Set("Cache-Control", "max-age=10")

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

	// add up link
	if r.URL.Path != "/" {
		var up string
		if strings.HasSuffix(r.URL.Path, "/") {
			up = ".."
		} else {
			up = path.Join("..", path.Base(path.Join(r.URL.Path, ".."))) + "/"
		}
		rawDoc = append(rawDoc, []byte("<a href=\""+up+"\" class=\"up-arrow\">\u21b0 up</a>")...)
	}

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

	// render and minify HTML
	var unminified bytes.Buffer
	html.Render(&unminified, doc)
	m.Minify("text/html", w, &unminified)
	w.WriteHeader(200)
}

func main() {
	if len(os.Args) != 2 {
		panic("you need to specify a base directory")
	}
	locks = make(map[string][]byte)
	lockExpires = make(map[string]time.Time)
	log.Fatal(http.ListenAndServe("127.0.0.1:8002", etag.Handler(http.HandlerFunc(handle), true)))
}
