package main

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/nscuro/dtrack-client"
)

func main() {
	var (
		url          string
		password     string
		projectCount int
		bomFilePath  string
	)
	flag.StringVar(&url, "url", "", "Dependency-Track URL")
	flag.StringVar(&password, "pass", "", "Dependency-Track admin password")
	flag.IntVar(&projectCount, "count", 10, "Target project count")
	flag.StringVar(&bomFilePath, "bom", "", "BOM file path")
	flag.Parse()

	dc, err := dtrack.NewClient(url)
	if err != nil {
		log.Fatalf("failed to initialize client: %v", err)
	}

	ctx := context.Background()

	log.Println("waiting for dtrack to be ready")
	waitCtx, _ := context.WithTimeout(ctx, 1*time.Minute)
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

	_ = token

	dc, err = dtrack.NewClient(url, dtrack.WithAPIKey("JylzNkUhKH3jbAZhBwxV7OOguGbRKpek"))
	if err != nil {
		log.Fatalf("failed to initialize authenticated client: %v", err)
	}

	log.Println("fetching projects")
	projectsPage, err := dc.Project.GetAll(ctx, dtrack.PageOptions{PageNumber: 1, PageSize: 1})
	if err != nil {
		log.Fatalf("failed to get projects: %v", err)
	}

	log.Printf("found %d projects, want %d", projectsPage.TotalCount, projectCount)
	if projectsPage.TotalCount < projectCount {
		diff := projectCount - projectsPage.TotalCount
		log.Printf("creating %d projects", diff)

		log.Printf("reading bom %s", bomFilePath)
		bomContent, err := os.ReadFile(bomFilePath)
		if err != nil {
			log.Fatalf("failed to read bom: %v", err)
		}

		bomEncoded := base64.StdEncoding.EncodeToString(bomContent)

		for i := 0; i < diff; i++ {
			log.Printf("creating project %d/%d", i+1, diff)
			_, err = dc.BOM.Upload(ctx, dtrack.BOMUploadRequest{
				ProjectName:    "Dependency-Track",
				ProjectVersion: uuid.NewString(),
				BOM:            bomEncoded,
				AutoCreate:     true,
			})
			if err != nil {
				log.Fatalf("failed to upload project: %v", err)
			}
		}
	} else {
		diff := projectsPage.TotalCount - projectCount
		log.Printf("deleting first %d projects", diff)

		projectsPage, err = dc.Project.GetAll(ctx, dtrack.PageOptions{PageNumber: 1, PageSize: diff})
		if err != nil {
			log.Fatalf("failed to fetch %d projects: %v", diff, err)
		}

		for i, project := range projectsPage.Projects {
			log.Printf("deleting project %s (%d/%d)", project.UUID, i+1, diff)
			err = dc.Project.Delete(ctx, project.UUID)
			if err != nil {
				log.Fatalf("failed to delete project %s: %v", project.UUID, err)
			}
		}
	}
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
