package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"

	_ "github.com/lib/pq"
)

const dbConnString = "user=postgres password=aaaaaaaaaa dbname=webrtc_tv sslmode=disable host=localhost port=5432"

func main() {
	db, err := sql.Open("postgres", dbConnString)
	if err != nil {
		log.Fatal("Failed to connect to database:", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		log.Fatal("Database ping failed:", err)
	}
	log.Println("Connected to PostgreSQL DB")

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "index.html")
	})

	// New API endpoint to fetch stations
	http.HandleFunc("/api/stations", func(w http.ResponseWriter, r *http.Request) {
		rows, err := db.Query("SELECT name FROM stations ORDER BY name ASC")
		if err != nil {
			log.Printf("Failed to query stations: %v", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		var stations []string
		for rows.Next() {
			var station string
			if err := rows.Scan(&station); err != nil {
				log.Printf("Failed to scan station: %v", err)
				continue
			}
			stations = append(stations, station)
		}

		if err := rows.Err(); err != nil {
			log.Printf("Error iterating stations: %v", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(stations)
	})

	log.Println("User server running on :80")
	log.Fatal(http.ListenAndServe(":80", nil))
}