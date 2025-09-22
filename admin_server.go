package main

import (
    "database/sql"
    "log"
    "net/http"
    "github.com/gin-gonic/gin"
    _ "github.com/lib/pq" // For PostgreSQL
)

func main() {
    r := gin.Default()

    // Load HTML templates (assuming admin.html is in the same directory or a "templates" folder)
    r.LoadHTMLGlob("*.html") // Or r.LoadHTMLFiles("admin.html") if only one file

    // DB connection (replace with your creds)
    db, err := sql.Open("postgres", "user=admin dbname=metadata sslmode=disable")
    if err != nil {
        log.Fatal(err)
    }
    defer db.Close()

    // Example endpoint: Add tag to file
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

    // Serve HTML interface at /
    r.GET("/", func(c *gin.Context) {
        // Render admin HTML form (embed or load from file)
        c.HTML(http.StatusOK, "admin.html", nil)
    })

    r.Run(":8082") // Separate port
}