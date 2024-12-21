package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"text/template"
	"time"

	"github.com/bitvora/go-bitvora"
)

type WebhookPayload struct {
	Event string `json:"event"`
	Data  struct {
		AmountSats         int       `json:"amount_sats"`
		ChainTxID          *string   `json:"chain_tx_id"`
		CreatedAt          time.Time `json:"created_at"`
		FeeSats            float64   `json:"fee_sats"`
		ID                 string    `json:"id"`
		LightningInvoiceID string    `json:"lightning_invoice_id"`
		Metadata           *Metadata `json:"metadata"`
		NetworkType        string    `json:"network_type"`
		RailType           string    `json:"rail_type"`
		Recipient          string    `json:"recipient"`
		Status             string    `json:"status"`
		UpdatedAt          time.Time `json:"updated_at"`
	} `json:"data"`
}

type Metadata struct {
	Npub string `json:"npub"`
}

type Response struct {
	Status  int         `json:"status"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

type PageData struct {
	Header      string
	Description string
	Price       string
}

func JSONResponse(w http.ResponseWriter, status int, message string, data interface{}) {
	response := Response{
		Status:  status,
		Message: message,
		Data:    data,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func handleGenerateInvoice(w http.ResponseWriter, r *http.Request) {
	bitvoraApiKey := os.Getenv("BITVORA_API_KEY")
	pricePerYearStr := os.Getenv("PRICE_PER_YEAR")
	pricePerYearFloat, err := strconv.ParseFloat(pricePerYearStr, 64)
	if err != nil {
		JSONResponse(w, http.StatusBadRequest, "Invalid price per year", nil)
		return
	}

	// Parse the JSON body
	var requestData struct {
		Npub string `json:"npub"`
	}
	err = json.NewDecoder(r.Body).Decode(&requestData)
	if err != nil || requestData.Npub == "" {
		JSONResponse(w, http.StatusBadRequest, "Missing or invalid npub", nil)
		return
	}

	npub := requestData.Npub

	metadata := map[string]string{
		"npub": npub,
	}

	bitvoraClient := bitvora.NewBitvoraClient(bitvora.Mainnet, bitvoraApiKey)
	invoice, err := bitvoraClient.CreateLightningInvoice(pricePerYearFloat, string(bitvora.SATS), "1 year subscription", 3600, metadata)
	if err != nil {
		JSONResponse(w, http.StatusInternalServerError, "Error creating invoice", nil)
		return
	}

	var response struct {
		Invoice string `json:"invoice"`
	}

	response.Invoice = invoice.Data.PaymentRequest
	createSubscription(npub)

	JSONResponse(w, http.StatusOK, "Invoice generated", response)
}

func handleHomePage(w http.ResponseWriter, r *http.Request) {
	// Define the variables to pass to the template
	data := PageData{
		Header:      os.Getenv("RELAY_NAME"),
		Description: os.Getenv("RELAY_DESCRIPTION"),
		Price:       os.Getenv("PRICE_PER_YEAR"),
	}

	// Parse the template file
	tmpl, err := template.ParseFiles("static/index.html")
	if err != nil {
		http.Error(w, "Unable to load template", http.StatusInternalServerError)
		return
	}

	// Render the template with the provided data
	err = tmpl.Execute(w, data)
	if err != nil {
		http.Error(w, "Unable to render template", http.StatusInternalServerError)
	}
}

func handleBitvoraWebhook(w http.ResponseWriter, r *http.Request) {
	secret := os.Getenv("BITVORA_WEBHOOK_SECRET")
	signature := r.Header.Get("bitvora-signature")

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Error reading request body", http.StatusBadRequest)
		return
	}
	payload := string(bodyBytes)

	if validateWebhookSignature(payload, signature, secret) {
		fmt.Println("Valid signature")
		// Process webhook
		w.WriteHeader(http.StatusOK)

		var webhookPayload WebhookPayload
		if err := json.Unmarshal(bodyBytes, &webhookPayload); err != nil {
			fmt.Println("Error parsing webhook payload:", err)
			http.Error(w, "Error parsing webhook payload", http.StatusBadRequest)
			return
		}

		if webhookPayload.Event == "deposit.lightning.completed" {

			log.Println("Received deposit.lightning.completed event", webhookPayload.Data.ID)

			var metadata Metadata
			if webhookPayload.Data.Metadata != nil {
				metadata = *webhookPayload.Data.Metadata
				if metadata.Npub != "" {
					npub := metadata.Npub
					setPaidSubscription(npub)
					loadWhitelist()
				} else {
					fmt.Println("No npub in metadata")
					return
				}
			}
		}

	} else {
		fmt.Println("Invalid signature")
		http.Error(w, "Invalid signature", http.StatusUnauthorized)
	}
}

func handlePollPayment(w http.ResponseWriter, r *http.Request) {
	// Ensure the request method is POST
	if r.Method != http.MethodPost {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}

	// Parse the JSON body
	var requestData struct {
		Npub string `json:"npub"`
	}
	err := json.NewDecoder(r.Body).Decode(&requestData)
	if err != nil || requestData.Npub == "" {
		JSONResponse(w, http.StatusBadRequest, "Missing or invalid npub", nil)
		return
	}

	// Process the payment polling
	active := pollPayment(requestData.Npub)
	var response struct {
		Active bool `json:"active"`
	}

	response.Active = active
	JSONResponse(w, http.StatusOK, "Payment status", response)
}

func validateWebhookSignature(payload, signature, secret string) bool {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(payload))
	expectedSignature := hex.EncodeToString(h.Sum(nil))
	return expectedSignature == signature
}
