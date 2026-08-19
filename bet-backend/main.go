package main

import (
	"log"
	"os"

	"tchebet/bet-backend/cache"
	"tchebet/bet-backend/db"
	"tchebet/bet-backend/handlers"

	"github.com/gin-gonic/gin"
)

func main() {
	log.Println("Starting Tchebet Betting Backend...")

	// Initialize Database (And thats why db has to be a object, and this should be handled at db.go)
	postgresDB, err := db.InitDB()
	if err != nil {
		log.Fatalf("Critical error initializing database: %v", err)
	}
	defer postgresDB.Close()

	// Initialize Redis
	redisClient, err := cache.InitRedis()
	if err != nil {
		log.Fatalf("Critical error initializing Redis: %v", err)
	}
	defer redisClient.Close()

	// Setup Gin router
	r := gin.Default()

	// Register Routes
	handlers.RegisterRoutes(r)
	handlers.RegisterListingRoutes(r)

	// Run Server
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Tchebet Betting Backend running on port %s...", port)
	if err := r.Run(":" + port); err != nil {
		log.Fatalf("Server failed to run: %v", err)
	}
}
