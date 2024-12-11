package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"golang.org/x/sync/semaphore"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CycloneDX/cyclonedx-go"
	dtrack "github.com/DependencyTrack/client-go"
	"github.com/google/uuid"
)

func main() {
	var (
		url            string
		password       string
		projectCount   int
		bomFilesPath   string
		maxConcurrency int
		doWait         bool
		pollInterval   time.Duration
		waitTimeout    time.Duration
		delay          time.Duration
		requestTimeout time.Duration
		skipFailed     bool
	)
	flag.StringVar(&url, "url", "", "Dependency-Track URL")
	flag.StringVar(&password, "pass", "", "Dependency-Track admin password")
	flag.IntVar(&projectCount, "count", 10, "Target project count")
	flag.StringVar(&bomFilesPath, "boms", "", "BOMs file path")
	flag.IntVar(&maxConcurrency, "max-concurrency", 5, "Max concurrent BOM upload requests")
	flag.DurationVar(&pollInterval, "poll-interval", 1*time.Second, "Interval for polling completion status")
	flag.BoolVar(&doWait, "wait", false, "Wait for BOM processing to complete")
	flag.DurationVar(&waitTimeout, "wait-timeout", 5*time.Minute, "Wait timeout")
	flag.DurationVar(&delay, "delay", 0, "Delay between upload requests")
	flag.DurationVar(&requestTimeout, "request-timeout", 10*time.Second, "Request timeout")
	flag.BoolVar(&skipFailed, "skip-failed", false, "Skip BOMs that failed to upload")
	flag.Parse()

	clientOptions := []dtrack.ClientOption{
		dtrack.WithTimeout(requestTimeout),
		dtrack.WithUserAgent(getUserAgent()),
	}

	dc, err := dtrack.NewClient(url, clientOptions...)
	if err != nil {
		log.Fatalf("failed to initialize client: %v", err)
	}

	bomFilePaths, err := filepath.Glob(filepath.Join(bomFilesPath, "*.cdx.json"))
	if err != nil {
		log.Fatalf("failed to glob bom files in %s: %v", bomFilesPath, err)
	} else if len(bomFilePaths) == 0 {
		log.Fatalf("no bom files found in %s", bomFilesPath)
	} else {
		log.Printf("found %d bom files in %s", len(bomFilePaths), bomFilesPath)
	}

	ctx := context.Background()
	sem := semaphore.NewWeighted(int64(maxConcurrency))

	log.Println("waiting for dtrack to be ready")
	waitCtx, _ := context.WithTimeout(ctx, 1*time.Minute) // nolint:govet
	err = waitForDT(waitCtx, dc)
	if err != nil {
		log.Fatalf("failed to wait for dtrack: %v", err)
	}

	log.Println("authenticating")
	token, err := dc.User.Login(ctx, "admin", password)
	if err != nil {
		var apiErr *dtrack.APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusUnauthorized {
			log.Fatalf("failed to authenticate: %v", err)
		}

		log.Println("probably first launch, changing admin password")
		err = dc.User.ForceChangePassword(ctx, "admin", "admin", password)
		if err != nil {
			log.Fatalf("failed to change admin password: %v", err)
		}

		log.Println("re-attempting login")
		token, err = dc.User.Login(ctx, "admin", password)
		if err != nil {
			log.Fatalf("failed to authenticate: %v", err)
		}
	}

	dc, err = dtrack.NewClient(url, append(clientOptions, dtrack.WithBearerToken(token))...)
	if err != nil {
		log.Fatalf("failed to initialize authenticated client: %v", err)
	}

	log.Println("fetching projects")
	projectsPage, err := dc.Project.GetAll(ctx, dtrack.PageOptions{PageNumber: 1, PageSize: 1})
	if err != nil {
		log.Fatalf("failed to get projects: %v", err)
	}

	start := time.Now()
	var totalUploads int32
	var failedUploads int32
	log.Printf("found %d projects, want %d", projectsPage.TotalCount, projectCount)
	if projectsPage.TotalCount < projectCount {
		diff := projectCount - projectsPage.TotalCount
		log.Printf("creating %d projects", diff)

		wg := &sync.WaitGroup{}
		waitCtx, _ = context.WithTimeout(context.TODO(), waitTimeout) // nolint:govet

		for i := 0; i < diff; i++ {
			err = sem.Acquire(ctx, 1)
			if err != nil {
				log.Fatalf("failed to acquire semaphore: %v", err)
			}

			go func() {
				defer sem.Release(1)

				bomFilePath := bomFilePaths[(i+1)%len(bomFilePaths)]
				log.Printf("reading bom %s", bomFilePath)
				bomContent, err := os.ReadFile(bomFilePath)
				if err != nil {
					log.Fatalf("failed to read bom: %v", err)
				}

				var bom cyclonedx.BOM
				err = cyclonedx.NewBOMDecoder(bytes.NewReader(bomContent), cyclonedx.BOMFileFormatJSON).Decode(&bom)
				if err != nil {
					log.Fatalf("failed to decode bom: %v", err)
				}

				projectName := "Dependency-Track"
				projectVersion := uuid.NewString()

				// Use project name and version from BOM if possible.
				if bom.Metadata != nil && bom.Metadata.Component != nil {
					if bom.Metadata.Component.Name != "" {
						projectName = ""
						if bom.Metadata.Component.Group != "" {
							projectName += bom.Metadata.Component.Group + "_"
						}
						projectName += bom.Metadata.Component.Name
					}
					if bom.Metadata.Component.Version != "" {
						projectVersion = bom.Metadata.Component.Version + "_" + projectVersion
					}
				}

				atomic.AddInt32(&totalUploads, 1)
				log.Printf("creating project %d/%d", i+1, diff)
				token, uploadErr := dc.BOM.PostBom(ctx, dtrack.BOMUploadRequest{
					ProjectName:    projectName,
					ProjectVersion: projectVersion,
					BOM:            string(bomContent),
					AutoCreate:     true,
				})
				if uploadErr != nil {
					if skipFailed {
						atomic.AddInt32(&failedUploads, 1)
						log.Printf("failed to upload project: %v", uploadErr)
						log.Printf("%d/%d uploads failed so far", failedUploads, totalUploads)
						return
					}

					log.Fatalf("failed to upload project: %v", uploadErr)
				}
				if doWait {
					wg.Add(1)
					go func() {
						defer wg.Done()
						start := time.Now()
						waitErr := waitForToken(waitCtx, dc, token, pollInterval)
						if waitErr != nil {
							log.Printf("waiting for token %s failed after %s: %v", token, time.Since(start), waitErr)
						} else {
							log.Printf("token %s processed after %s", token, time.Since(start))
						}
					}()
				}

				if delay > 0 && (i+1) < diff {
					time.Sleep(delay)
				}
			}()
		}

		err = sem.Acquire(ctx, int64(maxConcurrency))
		if err != nil {
			log.Fatalf("failed waiting for all upload requests to complete")
		}
		log.Printf("all upload requests sent after %s", time.Since(start))

		if doWait {
			wg.Wait()
			log.Printf("all done after %s", time.Since(start))
		}
	} else if projectsPage.TotalCount > projectCount {
		diff := projectsPage.TotalCount - projectCount
		log.Printf("deleting first %d projects", diff)

		projectsPage, err = dc.Project.GetAll(ctx, dtrack.PageOptions{PageNumber: 1, PageSize: diff})
		if err != nil {
			log.Fatalf("failed to fetch %d projects: %v", diff, err)
		}

		for i, project := range projectsPage.Items {
			log.Printf("deleting project %s (%d/%d)", project.UUID, i+1, diff)
			err = dc.Project.Delete(ctx, project.UUID)
			if err != nil {
				log.Fatalf("failed to delete project %s: %v", project.UUID, err)
			}
		}
	} else {
		log.Println("nothing to do")
	}
}

func getUserAgent() string {
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Path == "" {
		return "github.com/nscuro/dtbench"
	}

	return fmt.Sprintf("%s@%s", info.Main.Path, info.Main.Version)
}

func waitForDT(ctx context.Context, dc *dtrack.Client) error {
	ticker := time.NewTicker(3 * time.Second)

	for {
		select {
		case <-ticker.C:
			conn, err := net.Dial("tcp", dc.BaseURL().Host)
			if err != nil {
				log.Printf("failed to establish tcp connection: %v", err)
				continue
			}
			_ = conn.Close()

			_, err = dc.About.Get(ctx)
			if err != nil {
				log.Printf("failed to get about: %v", err)
				continue
			}

			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func waitForToken(ctx context.Context, dc *dtrack.Client, token dtrack.BOMUploadToken, pollInterval time.Duration) error {
	ticker := time.NewTicker(pollInterval)

	for {
		select {
		case <-ticker.C:
			processing, err := dc.BOM.IsBeingProcessed(ctx, token)
			if err != nil {
				return err
			}
			if !processing {
				return nil
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
