package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/mrcyjanek/space/lib/geolookup"
)

func main() {
	pbf := flag.String("pbf", "", "OSM PBF (runs osmium tags-filter + export)")
	admin := flag.String("admin", "", "admin GeoJSON / GeoJSONSeq")
	places := flag.String("places", "", "point-locality GeoJSON / GeoJSONSeq")
	out := flag.String("out", "", "output directory")
	work := flag.String("work", "", "scratch dir for -pbf (default: temp)")
	simplifyM := flag.Float64("simplify-m", geolookup.DefaultSimplifyM, "VW characteristic length in meters (0 disables)")
	minArea := flag.Uint64("min-area", 0, "override VW 2*area in quantized units (0 = from -simplify-m)")
	jobs := flag.Int("jobs", 0, "parallel workers for pack (0 = GOMAXPROCS)")
	keepWork := flag.Bool("keep-work", false, "do not delete -pbf scratch files")
	flag.Parse()
	if *out == "" {
		fatal("-out is required")
	}
	opts := geolookup.PackOptions{Jobs: *jobs}
	switch {
	case *minArea > 0:
		opts.MinArea = *minArea
	case *simplifyM > 0:
		opts.MinArea = geolookup.MinAreaFromMeters(*simplifyM, geolookup.DefaultScale)
	}
	adminPath, placesPath := *admin, *places
	if *pbf != "" {
		dir := *work
		if dir == "" {
			var err error
			dir, err = os.MkdirTemp("", "geolookup-build-*")
			if err != nil {
				fatal(err)
			}
			if !*keepWork {
				defer os.RemoveAll(dir)
			}
		}
		fmt.Fprintf(os.Stderr, "extracting from %s (work=%s)\n", *pbf, dir)
		if err := extractPBF(*pbf, dir); err != nil {
			fatal(err)
		}
		adminPath = filepath.Join(dir, "admin.geojsons")
		placesPath = filepath.Join(dir, "places.geojsons")
	}
	if adminPath == "" && placesPath == "" {
		fatal("need -pbf or -admin/-places")
	}
	if err := geolookup.Pack(adminPath, placesPath, *out, opts); err != nil {
		fatal(err)
	}
}

func extractPBF(pbf, work string) error {
	adminCfg := filepath.Join(work, "export-admin.json")
	placesCfg := filepath.Join(work, "export-places.json")
	if err := os.WriteFile(adminCfg, geolookup.OsmiumExportAdmin, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(placesCfg, geolookup.OsmiumExportPlaces, 0o644); err != nil {
		return err
	}
	adminPBF := filepath.Join(work, "admin.pbf")
	placesPBF := filepath.Join(work, "places.pbf")

	fmt.Fprintf(os.Stderr, "osmium tags-filter: admin + places (parallel)\n")
	if err := parallel(
		func() error {
			return run("osmium", "tags-filter", "--progress", pbf, "nwr/boundary=administrative",
				"-o", adminPBF, "--overwrite")
		},
		func() error {
			return run("osmium", "tags-filter", "--progress", pbf, "n/place=suburb", "n/place=neighbourhood",
				"n/place=quarter", "n/place=locality", "-o", placesPBF, "--overwrite")
		},
	); err != nil {
		return err
	}

	adminIdx := filepath.Join(work, "admin.nodes.idx")
	placesIdx := filepath.Join(work, "places.nodes.idx")
	fmt.Fprintf(os.Stderr, "osmium export: admin + places (parallel)\n")
	if err := parallel(
		func() error {
			return run("osmium", "export", "--progress", "-c", adminCfg, "-f", "geojsonseq",
				"--geometry-types=polygon", "-x", "print_record_separator=false",
				"-i", "sparse_file_array,"+adminIdx,
				"-o", filepath.Join(work, "admin.geojsons"), "-O", adminPBF)
		},
		func() error {
			return run("osmium", "export", "--progress", "-c", placesCfg, "-f", "geojsonseq",
				"--geometry-types=point", "-x", "print_record_separator=false",
				"-i", "sparse_file_array,"+placesIdx,
				"-o", filepath.Join(work, "places.geojsons"), "-O", placesPBF)
		},
	); err != nil {
		return err
	}
	_ = os.Remove(adminPBF)
	_ = os.Remove(placesPBF)
	_ = os.Remove(adminIdx)
	_ = os.Remove(placesIdx)
	return nil
}

func parallel(fns ...func() error) error {
	errCh := make(chan error, len(fns))
	var wg sync.WaitGroup
	for _, fn := range fns {
		wg.Add(1)
		go func(fn func() error) {
			defer wg.Done()
			errCh <- fn()
		}(fn)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}

func run(args ...string) error {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	fmt.Fprintf(os.Stderr, "+ %s\n", cmd.String())
	return cmd.Run()
}

func fatal(v any) {
	fmt.Fprintln(os.Stderr, v)
	os.Exit(1)
}
