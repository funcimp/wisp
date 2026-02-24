package main

import (
	"fmt"
	"net/http"
	"os"
)

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "hello from wisp")
	})
	fmt.Fprintln(os.Stderr, "helloworld: listening on :8080")
	if err := http.ListenAndServe(":8080", nil); err != nil { //#nosec G114 -- this is just a test server
		fmt.Fprintln(os.Stderr, "helloworld:", err)
		os.Exit(1)
	}
}
