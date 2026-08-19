package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
)

// RDB feels like something you would want on a constructor? But thats only if the logic scales, maybe Im wrong
// Idk much about redis
var RDB *redis.Client
var ctx = context.Background()

func InitRedis() (*redis.Client, error) {
	redisHost := getEnv("REDIS_HOST", "localhost:6379")

	rdb := redis.NewClient(&redis.Options{
		Addr:     redisHost,
		Password: "", // no password set
		DB:       0,  // use default DB
	})

	// Ping to verify connection
	_, err := rdb.Ping(ctx).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to redis: %w", err)
	}

	RDB = rdb
	log.Println("Redis connected successfully.")
	return rdb, nil
}

// PublishMarketUpdate publishes a market event to the Redis Pub/Sub channel
func PublishMarketUpdate(marketID string, eventType string, data interface{}) {
	if RDB == nil {
		return
	}

	// Setting the payload as a interface is smart
	payload := map[string]interface{}{
		"market_id":  marketID,
		"event_type": eventType,
		"timestamp":  time.Now().Unix(),
		"data":       data,
	}

	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		log.Printf("failed to marshal market update event: %v", err)
		return
	}

	err = RDB.Publish(ctx, "market_updates", jsonBytes).Err()
	if err != nil {
		log.Printf("failed to publish to Redis channel market_updates: %v", err)
	}
}

// CacheOdds caches the live odds for a market
func CacheOdds(marketID string, odds map[string]float64) {
	if RDB == nil {
		return
	}

	jsonBytes, err := json.Marshal(odds)
	if err != nil {
		return
	}

	key := fmt.Sprintf("market:%s:odds", marketID)
	RDB.Set(ctx, key, jsonBytes, 24*time.Hour)
}

// GetCachedOdds gets cached odds from Redis
func GetCachedOdds(marketID string) (map[string]float64, error) {
	if RDB == nil {
		return nil, fmt.Errorf("redis client not initialized") // Is this the only errror that could happen in this case? This could be confusing later
	}

	key := fmt.Sprintf("market:%s:odds", marketID)
	val, err := RDB.Get(ctx, key).Result()
	if err != nil {
		return nil, err
	}

	var odds map[string]float64
	err = json.Unmarshal([]byte(val), &odds)
	if err != nil {
		return nil, err
	}

	return odds, nil
}

// If this getEnv thing repeats more then twice in different files, I want a separate file with it as a shared object. 
// It would be moreDRY, and easier to change/modify
func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}
