package main

import (
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/gomarkdown/markdown"
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

	header, err := readExt(".header")
	if err == nil {
		w.Write(header)
	}

	var auth, file, md []byte

	if strings.HasPrefix(r.URL.Path, "/.auth/") {
		auth := strings.TrimPrefix(r.URL.Path, "/.auth/")
		http.SetCookie(w, &http.Cookie{Name: "nerka", Value: auth, Path: "/", Secure: true, HttpOnly: true, MaxAge: 31536000})
		http.Redirect(w, r, "/", http.StatusSeeOther)
		goto footer
	}

	auth, err = read(".auth")
	if err == nil {
		cookie, err := r.Cookie("nerka")
		if err != nil || cookie.Value != strings.TrimSpace(string(auth)) {
			w.Write([]byte("no"))
			goto footer
		}
	}

	if r.URL.Path == "/" {
		file, err = readExt("index")
	} else {
		file, err = readExt(r.URL.Path)
	}

	if err != nil {
		w.Write([]byte(err.Error()))
		goto footer
	}

	md = markdown.ToHTML(file, nil, nil)
	w.Write(md)

footer:
	footer, err := readExt(".footer")
	if err == nil {
		w.Write(footer)
	}
}

func main() {
	log.Fatal(http.ListenAndServe("127.0.0.1:8002", http.HandlerFunc(handle)))
}
