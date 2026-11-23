// Command datapack turns the address table and the vendor list netryx ships
// into the binary packs the service maps read-only on the router, so the two
// tables cost it no heap (internal/packfile, internal/enrich/pack.go,
// internal/inventory/ouipack.go).
//
// It is a build step, run by package/netryx/build.sh on the build host:
//
//	go run ./tools/datapack -ip2asn data/ip2asn-combined.tsv.gz -oui data/oui.txt -out <dir>
//
// writes <dir>/ip2asn.pack and <dir>/oui.pack. Each pack is read back and
// compared with the table it was made from before it is renamed into place,
// so a failed run writes nothing and a pack that would answer differently is
// never shipped.
//
//	go run ./tools/datapack -verify [-kind geo|oui] <file>...
//
// checks each file the way the service does before it uses one, and exits
// non-zero when any is damaged, cut short or not the kind asked for.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"netryx/internal/enrich"
	"netryx/internal/inventory"
	"netryx/internal/packfile"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("datapack", flag.ContinueOnError)
	fs.SetOutput(stderr)
	ip2asn := fs.String("ip2asn", "", "the iptoasn table (ip2asn-combined.tsv.gz) to pack")
	oui := fs.String("oui", "", "the IEEE oui.txt to pack")
	out := fs.String("out", "", "directory the packs are written to")
	verify := fs.Bool("verify", false, "check the files named after the flags instead of writing packs")
	kind := fs.String("kind", "", "with -verify: the kind every file must be, geo or oui")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *verify {
		if fs.NArg() == 0 {
			fmt.Fprintln(stderr, "datapack -verify: name at least one file")
			return 2
		}
		bad := 0
		for _, f := range fs.Args() {
			msg, err := verifyFile(f, *kind)
			if err != nil {
				fmt.Fprintf(stderr, "%s: %v\n", f, err)
				bad++
				continue
			}
			fmt.Fprintf(stdout, "%s: %s\n", f, msg)
		}
		if bad > 0 {
			return 1
		}
		return 0
	}
	if fs.NArg() > 0 || *out == "" || (*ip2asn == "" && *oui == "") {
		fmt.Fprintln(stderr, "usage: datapack [-ip2asn FILE] [-oui FILE] -out DIR | datapack -verify [-kind geo|oui] FILE...")
		return 2
	}
	if *ip2asn != "" {
		msg, err := packGeo(*ip2asn, filepath.Join(*out, "ip2asn.pack"))
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", *ip2asn, err)
			return 1
		}
		fmt.Fprintln(stdout, msg)
	}
	if *oui != "" {
		msg, err := packOUI(*oui, filepath.Join(*out, "oui.pack"))
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", *oui, err)
			return 1
		}
		fmt.Fprintln(stdout, msg)
	}
	return 0
}

// verifyFile checks one pack as the service would before it answers from it.
func verifyFile(path, kind string) (string, error) {
	isGeo := packfile.HasMagic(path, enrich.GeoMagic)
	isOUI := packfile.HasMagic(path, inventory.OUIMagic)
	switch {
	case kind == "geo" && !isGeo, kind == "oui" && !isOUI:
		return "", fmt.Errorf("not a %s pack", kind)
	case kind != "" && kind != "geo" && kind != "oui":
		return "", fmt.Errorf("unknown kind %q: geo or oui", kind)
	case isGeo:
		g, err := enrich.OpenGeoPack(path)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("address table, %d ranges, %d source lines skipped", g.Len(), g.Skipped()), nil
	case isOUI:
		p, err := inventory.OpenOUIPack(path)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("vendor list, %d registrations", p.Len()), nil
	}
	return "", fmt.Errorf("not a netryx data pack")
}

func packGeo(src, dst string) (string, error) {
	f, err := os.Open(src)
	if err != nil {
		return "", err
	}
	g, err := enrich.LoadIP2ASN(f)
	f.Close()
	if err != nil {
		return "", err
	}
	b, err := g.MarshalPack()
	if err != nil {
		return "", err
	}
	back, err := enrich.ReadGeoPack(b)
	if err != nil {
		return "", fmt.Errorf("the pack written does not read back: %w", err)
	}
	if err := enrich.SamePack(g, back); err != nil {
		return "", fmt.Errorf("the pack written answers differently: %w", err)
	}
	if err := packfile.WriteFile(dst, b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s: %d ranges, %d lines skipped, %d bytes", dst, g.Len(), g.Skipped(), len(b)), nil
}

func packOUI(src, dst string) (string, error) {
	f, err := os.Open(src)
	if err != nil {
		return "", err
	}
	m, err := inventory.LoadOUI(f)
	f.Close()
	if err != nil {
		return "", err
	}
	b, err := inventory.MarshalOUIPack(m)
	if err != nil {
		return "", err
	}
	back, err := inventory.ReadOUIPack(b)
	if err != nil {
		return "", fmt.Errorf("the pack written does not read back: %w", err)
	}
	if back.Len() != len(m) {
		return "", fmt.Errorf("the pack holds %d registrations, the file %d", back.Len(), len(m))
	}
	for k, v := range m {
		if got := back.Vendor(k); got != v {
			return "", fmt.Errorf("the pack names %s %q, the file %q", k, got, v)
		}
	}
	if err := packfile.WriteFile(dst, b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s: %d registrations, %d bytes", dst, len(m), len(b)), nil
}
