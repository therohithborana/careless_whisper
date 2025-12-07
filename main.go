package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

var (
	sendTimes   = make(map[string]time.Time)
	sendTimesMu sync.Mutex

	lastMessageCache      = make(map[string]types.MessageInfo)
	lastMessageCacheMu    sync.Mutex

	csvWriter *csv.Writer
	csvFile   *os.File
	
	// --- Experiment settings ---
	// TODO: Set these before running
	targetJIDString = "9481818626@s.whatsapp.net" // Replace with target phone number
	probeInterval   = 2 * time.Minute             // How often to send reaction probes
	emojiList       = []string{"👍", "❤️", "😊", "🔥", "👀"} // Emojis to cycle through
	emojiIndex      = 0
	// --- End settings ---
)

func initCSV() {
	var err error
	csvFile, err = os.OpenFile("user_activity_log.csv", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatalf("Failed to open CSV file: %v", err)
	}

	info, err := csvFile.Stat()
	if err != nil {
		log.Fatalf("Failed to stat CSV file: %v", err)
	}
	
	csvWriter = csv.NewWriter(csvFile)
	
	if info.Size() == 0 {
		header := []string{"timestamp", "probe_sent_ts", "receipt_recv_ts", "rtt_ms", "receipt_type", "user_status", "emoji_used"}
		csvWriter.Write(header)
		csvWriter.Flush()
	}
}

func handleEvent(client *whatsmeow.Client, evt interface{}) {
	switch v := evt.(type) {
	case *events.Message:
		// Update last message cache when new messages arrive from target
		jidStr := v.Info.Chat.String()
		if jidStr == targetJIDString && !v.Info.IsFromMe {
			lastMessageCacheMu.Lock()
			lastMessageCache[jidStr] = v.Info
			lastMessageCacheMu.Unlock()
			log.Printf("Updated anchor message: New message from %s (ID: %s)", jidStr, v.Info.ID)
		}

	case *events.Receipt:
		handleReceipt(v)
	}
}

func handleReceipt(receipt *events.Receipt) {
	recvTime := time.Now().UTC()
	receiptType := "unknown"
	userStatus := "unknown"

	switch receipt.Type {
	case types.ReceiptTypeDelivered:
		receiptType = "delivered"
		userStatus = "offline_or_no_internet"
	case types.ReceiptTypeRead:
		receiptType = "read"
		userStatus = "active_reading"
	case types.ReceiptTypePlayed:
		receiptType = "played"
		userStatus = "active_viewing"
	}

	for _, msgID := range receipt.MessageIDs {
		sendTimesMu.Lock()
		tSend, ok := sendTimes[msgID]
		if ok {
			delete(sendTimes, msgID)
		}
		sendTimesMu.Unlock()

		if !ok {
			continue
		}

		rtt := recvTime.Sub(tSend)
		rttMs := float64(rtt.Microseconds()) / 1000.0

		record := []string{
			time.Now().UTC().Format(time.RFC3339),
			tSend.Format(time.RFC3339Nano),
			recvTime.Format(time.RFC3339Nano),
			fmt.Sprintf("%.3f", rttMs),
			receiptType,
			userStatus,
			"", // emoji field - will be filled if needed
		}
		csvWriter.Write(record)
		csvWriter.Flush()

		fmt.Printf("[%s] User Status: %s | RTT: %.3f ms | Receipt: %s\n",
			time.Now().Format("15:04:05"), userStatus, rttMs, receiptType)
			
		// Analyze activity based on RTT
		analyzeUserActivity(rttMs, receiptType)
	}
}

func analyzeUserActivity(rttMs float64, receiptType string) {
	// Based on research: Different RTT ranges indicate different activity states
	if receiptType == "read" {
		if rttMs < 100 {
			log.Printf("  → Analysis: User likely has screen ON, app in FOREGROUND (Fast RTT)")
		} else if rttMs < 500 {
			log.Printf("  → Analysis: User likely has screen ON, app in BACKGROUND")
		} else if rttMs < 2000 {
			log.Printf("  → Analysis: User likely has screen OFF or device in low power mode")
		} else {
			log.Printf("  → Analysis: User likely on slow connection or device sleeping")
		}
	}
}

func sendReactionProbe(client *whatsmeow.Client, jid types.JID, anchorMessageInfo types.MessageInfo) {
	tSend := time.Now().UTC()
	
	// Cycle through emojis
	emoji := emojiList[emojiIndex%len(emojiList)]
	emojiIndex++

	msg := &waProto.Message{
		ReactionMessage: &waProto.ReactionMessage{
			Key: &waProto.MessageKey{
				RemoteJID: proto.String(jid.String()),
				FromMe:    proto.Bool(anchorMessageInfo.IsFromMe),
				ID:        proto.String(anchorMessageInfo.ID),
			},
			Text:              proto.String(emoji),
			SenderTimestampMS: proto.Int64(tSend.UnixMilli()),
		},
	}

	if jid.Server == types.GroupServer {
		msg.ReactionMessage.Key.Participant = proto.String(anchorMessageInfo.Sender.String())
	}

	resp, err := client.SendMessage(context.Background(), jid, msg)
	if err != nil {
		log.Printf("Failed to send reaction probe: %v", err)
		return
	}

	sendTimesMu.Lock()
	sendTimes[resp.ID] = tSend
	sendTimesMu.Unlock()

	log.Printf("Sent reaction probe '%s' with ID: %s", emoji, resp.ID)
}

func getLastMessageFromChat(client *whatsmeow.Client, jid types.JID) (types.MessageInfo, error) {
	// First check cache
	lastMessageCacheMu.Lock()
	if cached, ok := lastMessageCache[jid.String()]; ok {
		lastMessageCacheMu.Unlock()
		return cached, nil
	}
	lastMessageCacheMu.Unlock()

	// Try to get history (this may not work in all whatsmeow versions)
	// As fallback, we'll wait for incoming messages to update cache
	return types.MessageInfo{}, fmt.Errorf("no cached message found - waiting for new messages")
}

func main() {
	initCSV()
	defer csvFile.Close()

	dbLog := waLog.Stdout("Database", "ERROR", true)
	container, err := sqlstore.New(context.Background(), "sqlite3", "file:whisper.db?_foreign_keys=on", dbLog)
	if err != nil {
		panic(err)
	}

	deviceStore, err := container.GetFirstDevice(context.Background())
	if err != nil {
		panic(err)
	}

	clientLog := waLog.Stdout("Client", "ERROR", true)
	client := whatsmeow.NewClient(deviceStore, clientLog)
	
	// Add event handler with client reference
	client.AddEventHandler(func(evt interface{}) {
		handleEvent(client, evt)
	})

	if client.Store.ID == nil {
		qrChan, _ := client.GetQRChannel(context.Background())
		err = client.Connect()
		if err != nil {
			log.Printf("Connection initiated, waiting for QR scan...")
		}
		for evt := range qrChan {
			if evt.Event == "code" {
				qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
				fmt.Println("QR code generated. Scan with WhatsApp on your phone.")
			} else {
				fmt.Println("Login event:", evt.Event)
			}
		}
	} else {
		err = client.Connect()
		if err != nil {
			panic(err)
		}
	}

	targetJID, err := types.ParseJID(targetJIDString)
	if err != nil {
		log.Fatalf("Failed to parse target JID: %v. Edit 'targetJIDString' in code.", err)
	}

	log.Println("═══════════════════════════════════════════════════════")
	log.Println("  WhatsApp User Activity Tracker")
	log.Println("═══════════════════════════════════════════════════════")
	log.Printf("Target User: %s", targetJID)
	log.Printf("Probe Interval: %v", probeInterval)
	log.Printf("Output File: user_activity_log.csv")
	log.Println("═══════════════════════════════════════════════════════")
	log.Println("IMPORTANT: Send a message to the target user first,")
	log.Println("or wait for them to send you a message.")
	log.Println("The bot will react to their last message.")
	log.Println("═══════════════════════════════════════════════════════")

	// Wait a bit for messages to sync
	log.Println("Waiting 5 seconds for message sync...")
	time.Sleep(5 * time.Second)

	log.Println("Monitoring started. Press Ctrl+C to stop.")
	log.Println("═══════════════════════════════════════════════════════")

	ticker := time.NewTicker(probeInterval)
	defer ticker.Stop()

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)

	// Start probing routine
	go func() {
		for {
			// Try to get current anchor message
			currentAnchor, err := getLastMessageFromChat(client, targetJID)
			if err != nil {
				log.Printf("Waiting for messages from target user...")
				time.Sleep(10 * time.Second)
				continue
			}
			
			sendReactionProbe(client, targetJID, currentAnchor)
			
			// Wait for next interval
			<-ticker.C
		}
	}()

	<-c
	log.Println("\n═══════════════════════════════════════════════════════")
	log.Println("Shutdown signal received. Disconnecting...")
	client.Disconnect()
	log.Println("Disconnected. Activity log saved to user_activity_log.csv")
	log.Println("═══════════════════════════════════════════════════════")
}