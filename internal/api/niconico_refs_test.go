package api

import (
	"strconv"
	"testing"
)

func TestMapRefsFillsEveryRefWhileHandlesRunInParallel(t *testing.T) {
	refs := map[string]string{}
	for i := 0; i < 2000; i++ {
		refs["https://cdn.example/seg"+strconv.Itoa(i)+".ts"] = ""
	}
	refs["https://cdn.example/skip.ts"] = ""
	mapRefs(refs, 32, func(ref string) string {
		if ref == "https://cdn.example/skip.ts" {
			return ""
		}
		return "../media/" + ref[len("https://cdn.example/"):]
	})
	for ref, mapped := range refs {
		if ref == "https://cdn.example/skip.ts" {
			if mapped != "" {
				t.Fatalf("an unmapped ref got %q", mapped)
			}
			continue
		}
		if mapped == "" {
			t.Fatalf("%s was not mapped", ref)
		}
	}
}
