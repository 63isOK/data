package main

import (
	"golang.org/x/net/websocket"
	"fmt"
	"flag"
	"os"
	"net"
    // "io/ioutil"
    "time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v2"
)

var sdpOffer string

func init(){
	flag.StringVar(&sdpOffer, "sdp", "", "")
}

func main(){
	flag.Parse()
	fmt.Println(sdpOffer)

	origin := "http://localhost/"
	url := "ws://localhost:10101/ws"
	ws, err := websocket.Dial(url, "", origin)
	if err != nil {
		panic(err)
	}

	go func() {
        if _, err := ws.Write([]byte("ping-video")); err != nil {
            panic(err)
        }

        var msg = make([]byte, 512)

        var n int
        for {
            if n,err = ws.Read(msg); err != nil {
                panic(err)
            }

            fmt.Printf("recv: %s\n", msg[:n])
            
            msgString := string(msg[:n])
            if (msgString == "quit"){
                os.Exit(1)
            }
        }
    }()

    timer := time.NewTimer(10 * time.Second)
    go func() {
        select {
        case <- timer.C:
            if _, err := ws.Write([]byte("sdpv")); err != nil {
                panic(err)
            }
            os.Exit(1)
        }
    }()
    
//    sdp, err := ioutil.ReadFile("a.sdp")
//	  if err != nil {
//		panic(err)
//	  }
//    Decode(string(sdp), &offer)

	// Wait for the offer to be pasted
	offer := webrtc.SessionDescription{}
	Decode(sdpOffer, &offer)
    

	// We make our own mediaEngine so we can place the sender's codecs in it.  This because we must use the
	// dynamic media type from the sender in our answer. This is not required if we are the offerer
	mediaEngine := webrtc.MediaEngine{}
	err = mediaEngine.PopulateFromSDP(offer)
	if err != nil {
		panic(err)
	}
	var payloadType uint8
	for _, videoCodec := range mediaEngine.GetCodecsByKind(webrtc.RTPCodecTypeVideo) {
		if videoCodec.Name == "VP8" {
			payloadType = videoCodec.PayloadType
			break
		}
	}
	if payloadType == 0 {
		panic("Remote peer does not support VP8")
	}
	api := webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine))
	peerConnection, err := api.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{"stun:stun.l.google.com:19302"},
			},
		},
	})
	if err != nil {
		panic(err)
	}

	// Open a UDP Listener for RTP Packets on port 5004
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5004})
	if err != nil {
		panic(err)
	}
	defer func() {
		if err = listener.Close(); err != nil {
			panic(err)
		}
	}()

	fmt.Println("Waiting for RTP Packets, please run GStreamer or ffmpeg now")

	// Listen for a single RTP Packet, we need this to determine the SSRC
	inboundRTPPacket := make([]byte, 4096) // UDP MTU
	n, _, err := listener.ReadFromUDP(inboundRTPPacket)
	if err != nil {
		panic(err)
	}

    timer.Stop()

	// Unmarshal the incoming packet
	packet := &rtp.Packet{}
	if err = packet.Unmarshal(inboundRTPPacket[:n]); err != nil {
		panic(err)
	}

	// Create a video track, using the same SSRC as the incoming RTP Packet
	videoTrack, err := peerConnection.NewTrack(payloadType, packet.SSRC, "video", "pion")
	if err != nil {
		panic(err)
	}
	if _, err = peerConnection.AddTrack(videoTrack); err != nil {
		panic(err)
	}

	// Set the handler for ICE connection state
	// This will notify you when the peer has connected/disconnected
	peerConnection.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		fmt.Printf("Connection State has changed %s \n", connectionState.String())
	})

	// Set the remote SessionDescription
	if err = peerConnection.SetRemoteDescription(offer); err != nil {
		panic(err)
	}

	// Create answer
	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		panic(err)
	}

	// Sets the LocalDescription, and starts our UDP listeners
	if err = peerConnection.SetLocalDescription(answer); err != nil {
		panic(err)
	}

	// Output the answer in base64 so we can paste it in browser
    sdpv := "sdpv" + Encode(answer)
	fmt.Println(sdpv)

	if _, err := ws.Write([]byte(sdpv)); err != nil {
		panic(err)
	}

	// Read RTP packets forever and send them to the WebRTC Client
	for {
		n, _, err := listener.ReadFrom(inboundRTPPacket)
		if err != nil {
			fmt.Printf("error during read: %s", err)
			panic(err)
		}

		packet := &rtp.Packet{}
		if err := packet.Unmarshal(inboundRTPPacket[:n]); err != nil {
			panic(err)
		}
		packet.Header.PayloadType = payloadType

		if writeErr := videoTrack.WriteRTP(packet); writeErr != nil {
			panic(writeErr)
		}
	}
}