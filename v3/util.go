package grokker

import (
	"fmt"
	"io"
	"math"
	"os"
	"strings"

	. "github.com/stevegt/goadapt"
)

// copyFile copies a file from src to dst
func copyFile(src, dst string) (err error) {
	defer Return(&err)
	// open src file
	srcfh, err := os.Open(src)
	Ck(err)
	defer srcfh.Close()
	// ensure dst file does not exist
	_, err = os.Stat(dst)
	if err == nil {
		Fpf(os.Stderr, "Error: %s already exists\n", dst)
		os.Exit(1)
	}
	// open dst file with same mode as src
	fi, err := srcfh.Stat()
	Ck(err)
	dstfh, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fi.Mode())
	Ck(err)
	defer dstfh.Close()
	// copy
	_, err = io.Copy(dstfh, srcfh)
	Ck(err)
	return
}

// similarity returns the cosine similarity between two embeddings.
func similarity(a, b []float64) float64 {
	if len(a) != len(b) {
		return 0
	}
	var dot, magA, magB float64
	for i := range a {
		dot += a[i] * b[i]
		magA += a[i] * a[i]
		magB += b[i] * b[i]
	}
	return dot / (math.Sqrt(magA) * math.Sqrt(magB))
}

// meanSimilarity returns the mean cosine similarity between two sets of embeddings.
func XXXmeanSimilarity(a, b [][]float64) float64 {
	var sum float64
	for i := range a {
		for j := range b {
			sum += similarity(a[i], b[j])
		}
	}
	return sum / float64(len(a)*len(b))
}

// meanVector returns the mean vector of a set of embedding vectors.
func meanVector(vectors [][]float64) (mean []float64) {
	if len(vectors) == 0 {
		return
	}
	// get size of the first vector
	size := len(vectors[0])
	// initialize mean vector
	mean = make([]float64, size)
	// calculate mean vector by iterating over each dimension
	for dim := 0; dim < size; dim++ {
		var sum float64
		// iterate over each vector
		for i := 0; i < len(vectors); i++ {
			sum += vectors[i][dim]
		}
		mean[dim] = sum / float64(len(vectors))
	}
	return mean
}

// stringInSlice returns true if str is in list.
func stringInSlice(str string, list []string) bool {
	for _, v := range list {
		if v == str {
			return true
		}
	}
	return false
}

// Ext2Lang derives language from file extension.
func Ext2Lang(fn string) (lang string, known bool, err error) {
	// split on dots and take the last part
	parts := strings.Split(fn, ".")
	if len(parts) < 2 {
		err = fmt.Errorf("file %s missing language or extension", fn)
		return
	}
	lang = parts[len(parts)-1]
	// see if we can convert the file extension to a language name
	known = true
	switch lang {
	case "md":
		lang = "markdown"
	case "py":
		lang = "python"
	case "rb":
		lang = "ruby"
	case "rs":
		lang = "rust"
	case "go":
		lang = "go"
	default:
		known = false
	}
	return
}
