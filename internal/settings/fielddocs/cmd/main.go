package main

import (
	"github.com/theatarr/theatarr/internal/settings/fielddocs"
	"log"
	"os"
)

func main() {
	b, err := fielddocs.Generate("../policy")
	if err != nil {
		log.Fatal(err)
	}
	if err = os.WriteFile("sectionfields_gen.json", b, 0644); err != nil {
		log.Fatal(err)
	}
}
