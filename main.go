package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type Asset struct {
	Name       string
	Mu         float64 // Return (%)
	Sigma      float64 // Volatility (%)
	Spread     float64
	LastPrice  float64 // Initial Price (S0)
	Decimals   int     // Decimal places based on Spread
}

type Quote struct {
	Bid       float64 `json:"bid"`
	Ask       float64 `json:"ask"`
	Last      float64 `json:"last"`
	Timestamp string  `json:"timestamp"`
}

type Subscriber struct {
	Msgs chan []byte
}

type Server struct {
	SubscriberMessageBuffer int
	Mux                     http.ServeMux
	SubscribersMu           sync.Mutex
	Subscribers             map[*Subscriber]struct{}
}

func (s *Server) AddSubscriber(subscriber *Subscriber) {
	s.SubscribersMu.Lock()
	s.Subscribers[subscriber] = struct{}{}
	s.SubscribersMu.Unlock()
	log.Printf("Added subscriber %p\n", subscriber)
}

func (s *Server) DeleteSubscriber(subscriber *Subscriber) {
	s.SubscribersMu.Lock()
	delete(s.Subscribers, subscriber)
	s.SubscribersMu.Unlock()
	log.Printf("Deleted subscriber %p\n", subscriber)
}

func (s *Server) PublishMsg(msg any) {
	s.SubscribersMu.Lock()
	defer s.SubscribersMu.Unlock()

	jsonBytes, err := json.Marshal(msg)
	if err != nil {
		log.Printf("Error marshaling message: %v\n", err)
		return
	}

	for sub := range s.Subscribers {
		select {
		case sub.Msgs <- jsonBytes:
		default:
			log.Printf("Warning: subscriber channel full, message dropped\n")
		}
	}
}

func (s *Server) Subscribe(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	subscriber := &Subscriber{
		Msgs: make(chan []byte, s.SubscriberMessageBuffer),
	}
	s.AddSubscriber(subscriber)
	defer s.DeleteSubscriber(subscriber)

	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return fmt.Errorf("websocket accept error: %w", err)
	}
	defer c.CloseNow()

	log.Printf("New WebSocket connection established from %s\n", r.RemoteAddr)

	ctx = c.CloseRead(ctx)
	for {
		select {
		case msg := <-subscriber.Msgs:
			writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := c.Write(writeCtx, websocket.MessageText, msg)
			cancel()
			if err != nil {
				return fmt.Errorf("websocket write error: %w", err)
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *Server) SubscribeHandler(w http.ResponseWriter, r *http.Request) {
	if err := s.Subscribe(r.Context(), w, r); err != nil {
		log.Printf("Subscription ended: %v\n", err)
	}
}

func simulatePrice(asset *Asset) float64 {
	dt := 0.003968 // 1/252
	mu := asset.Mu / 100
	sigma := asset.Sigma / 100
	normalValue := rand.NormFloat64() // Generates N(0,1)

	newPrice := asset.LastPrice * (1 + mu*dt + sigma*math.Sqrt(dt)*normalValue)

	precision := math.Pow(10, float64(asset.Decimals))
	newPrice = math.Round(newPrice*precision) / precision

	asset.LastPrice = newPrice
	return newPrice
}

func (s *Server) startQuoteGenerator(ctx context.Context, assets []*Asset) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			for _, asset := range assets {
				last := simulatePrice(asset)
				precision := math.Pow(10, float64(asset.Decimals));
				quote := Quote{
					Bid:       math.Round((last - asset.Spread) * precision) / precision,
					Ask:       math.Round((last + asset.Spread) * precision) / precision,
					Last:      last,
					Timestamp: time.Now().Format("2006-01-02 15:04:05.000"),
				}
				s.PublishMsg(quote)
			}
		case <-ctx.Done():
			return
		}
	}
}

func NewServer() (*Server, []*Asset) {
	server := &Server{
		SubscriberMessageBuffer: 10,
		Subscribers:             make(map[*Subscriber]struct{}),
	}
	server.Mux.HandleFunc("/cotizador", server.SubscribeHandler)

	assets := []*Asset{
		{"WTI", 6, 47, 0.1, 70.00, 1},
		{"SOY", 8, 14, 0.25, 995.0, 3},
		{"YPF", 16, 46, 0.5, 25.0, 1},
		{"SP500", 10, 12, 5, 5700.0, 0},
	}
	return server, assets
}

func main() {
	rand.Seed(time.Now().UnixNano())

	server, assets := NewServer()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go server.startQuoteGenerator(ctx, assets)

	log.Println("Starting server on port 5000")
	if err := http.ListenAndServe(":5000", &server.Mux); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
