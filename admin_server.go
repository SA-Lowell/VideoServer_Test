package main

import
(
	"database/sql"
	"errors"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"github.com/gin-gonic/gin"
	_ "github.com/lib/pq"
)

func main() {
	r := gin.Default()

	// Custom recovery middleware to handle runtime panics as 500 or 404
	r.Use(customRecovery())

	// Safe template loading (no panic if no files)
	loadTemplatesSafely(r, "templates/*.html")

	// DB connection (replace with your creds)
	db, err := sql.Open("postgres", "user=admin dbname=metadata sslmode=disable")

	if err != nil {
		log.Fatal(err)
	}

	defer db.Close()

	// Set up public directory (like public_html) for static files with index handling
	publicDir := "./public" // Change to your public_html folder path
	r.StaticFS("/public_html", http.Dir(publicDir)) // Serve static files under /public_html

	// Example dynamic endpoint: Add tag to file
	r.POST("/files/:id/tags", func(c *gin.Context) {
		id := c.Param("id")
		var tags []string

		if err := c.BindJSON(&tags); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})

			return
		}

		// Update DB (use prepared stmt in production)
		_, err := db.Exec("UPDATE files SET tags = tags || $1 WHERE id = $2", tags, id)

		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})

			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true})
	})

	// Serve dynamic HTML interface at root (or use static if preferred)
	r.GET("/", func(c *gin.Context) {
		// Check if template exists before rendering
		if r.HTMLRender.Instance("admin.html", nil) == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Resource not found"})

			return
		}

		// Render if found
		c.HTML(http.StatusOK, "admin.html", gin.H{
			"Title": "Admin Panel", // Example dynamic data
		})
	})

	r.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, gin.H{"error": "404"})
	})

	r.Run(":8082")
}

// Custom recovery middleware: Handles panics, checks for template errors as 404
func customRecovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				// Convert panic to error
				var recoveredErr error

				switch x := err.(type) {
					case string:
						recoveredErr = errors.New(x)
					case error:
						recoveredErr = x
					default:
						recoveredErr = errors.New("unknown panic")
				}

				// Check if it's a template not found error (updated matching)
				errStr := recoveredErr.Error()

				if strings.Contains(errStr, "no template") || strings.Contains(errStr, "pattern matches no files") || strings.Contains(errStr, "template:") || strings.Contains(errStr, "undefined") {
					c.JSON(http.StatusNotFound, gin.H{"error": "Resource not found"})
				} else {
					// Default to 500 for other panics
					c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
				}

				c.Abort()
			}
		}()

		c.Next()
	}
}

// Safe template loading: No panic if no files match
func loadTemplatesSafely(r *gin.Engine, pattern string) {
	files, err := filepath.Glob(pattern)

	if err != nil {
		log.Printf("Warning: Failed to glob templates: %v", err)

		return
	}
	if len(files) == 0 {
		log.Printf("Warning: No templates found matching '%s'", pattern)

		return
	}

	r.LoadHTMLFiles(files...)
}