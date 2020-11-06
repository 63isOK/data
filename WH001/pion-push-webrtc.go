package main

import (
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"

	"golang.org/x/net/websocket"

	// "io/ioutil"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"
)

var (
  sdpOffer string
  mp4file string
  frameX int
	frameY int
)

func init(){
	flag.StringVar(&sdpOffer, "sdp", "", "网页sdp offer")
  flag.StringVar(&mp4file, "mp4", "d:\\5.mp4", "mp4文件路径")
  // flag.IntVar(&frameX, "w", 1920, "视频宽")
	// flag.IntVar(&frameY, "h", 1080, "视频高")
}

func main(){
	flag.Parse()
	// fmt.Println(mp4file)
	// fmt.Println(frameX, frameY)

	ffmpeg := exec.Command("ffmpeg",
											"-re",
											"-i",
											mp4file,
										  "-an",
											"-vcodec",
											"libx264",
											"-preset:v",
											"veryfast",
											"-tune:v",
											"zerolatency",
                      "-payload_type",
                      "102",
                      "-profile:v",
                      "baseline",
                      "-level",
                      "3.1",
											"-f",
											"rtp",
											"rtp://127.0.0.1:5006",
											"-vn",
											"-acodec",
											"libopus",
											"-f",
											"rtp",
											"rtp://127.0.0.1:5006")
	// ffmpegIn, _ := ffmpeg.StdinPipe()
	// ffmpegOut, _ := ffmpeg.StdoutPipe()
	if err := ffmpeg.Start(); err != nil {
		panic(err)
	}

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

	offer := webrtc.SessionDescription{}
	Decode(sdpOffer, &offer)

	mediaEngine := webrtc.MediaEngine{}
	err = mediaEngine.PopulateFromSDP(offer)
	if err != nil {
		panic(err)
	}
	var payloadTypeAudio uint8
	var payloadTypeVideo uint8
	for _, videoCodec := range mediaEngine.GetCodecsByKind(webrtc.RTPCodecTypeVideo) {
		if videoCodec.Name == "H264" && videoCodec.RTPCodecCapability.SDPFmtpLine == "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f" {
			payloadTypeVideo = videoCodec.PayloadType
			break;
		}
	}
	//payloadTypeVideo = 102
	for _, audioCodec := range mediaEngine.GetCodecsByKind(webrtc.RTPCodecTypeAudio) {
		if audioCodec.Name == "opus" {
			payloadTypeAudio = audioCodec.PayloadType
			break;
		}
	}
	if payloadTypeAudio == 0 || payloadTypeVideo == 0 {
		panic("Remote peer does not support VP8 or opus")
	}

	fmt.Println("h264:", payloadTypeVideo, "opus:", payloadTypeAudio)

	api := webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine))
	peerConnection, err := api.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			//{
			//	URLs: []string{"stun:stun.l.google.com:19302"},
			//},
		},
	})
	if err != nil {
		panic(err)
	}

	// Open a UDP Listener for RTP Packets on port 5004
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5006})
	if err != nil {
		panic(err)
	}
	defer func() {
		if err = listener.Close(); err != nil {
			panic(err)
		}
	}()

	listener.SetReadBuffer(204800)

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
	videoTrack, err := peerConnection.NewTrack(payloadTypeVideo, 123, "video", "pion")
	if err != nil {
		panic(err)
	}
	if _, err = peerConnection.AddTrack(videoTrack); err != nil {
		panic(err)
	}

	audioTrack, err := peerConnection.NewTrack(payloadTypeAudio, 456, "audio", "pion")
	if err != nil {
		panic(err)
	}
	if _, err = peerConnection.AddTrack(audioTrack); err != nil {
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

	gatherComplete := webrtc.GatheringCompletePromise(peerConnection)

	// Sets the LocalDescription, and starts our UDP listeners
	if err = peerConnection.SetLocalDescription(answer); err != nil {
		panic(err)
	}

	<- gatherComplete

	// Output the answer in base64 so we can paste it in browser
  sdpv := "sdpv" + Encode(answer)
	if fi, err := os.Create("answer.sdp"); err == nil {
		io.WriteString(fi, Encode(answer))
	}

	if _, err := ws.Write([]byte(sdpv)); err != nil {
		panic(err)
	}

	var track *webrtc.Track

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

		if packet.Header.PayloadType == 97 {
			packet.Header.PayloadType = payloadTypeAudio
			packet.SSRC = 456
			packet.Header.SSRC = 456
			track = audioTrack
		} else if packet.Header.PayloadType == 102 {
			packet.Header.PayloadType = payloadTypeVideo
			packet.SSRC = 123
			packet.Header.SSRC = 123
			track = videoTrack
		} else {
			fmt.Println("unknown payload type", packet.Header.PayloadType)
		}

		if writeErr := track.WriteRTP(packet); writeErr != nil {
			panic(writeErr)
		}
	}
}