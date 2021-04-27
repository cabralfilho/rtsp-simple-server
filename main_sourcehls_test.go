package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSourceHLS(t *testing.T) {
	p1, ok := testProgram(
		"rtmpDisable: yes\n")
	require.Equal(t, true, ok)
	defer p1.close()

	time.Sleep(1 * time.Second)

	cnt1, err := newContainer("ffmpeg", "source", []string{
		"-re",
		"-stream_loop", "-1",
		"-i", "emptyvideo.mkv",
		"-c", "copy",
		"-f", "rtsp",
		"rtsp://" + ownDockerIP + ":8554/stream",
	})
	require.NoError(t, err)
	defer cnt1.close()

	time.Sleep(3 * time.Second)

	p2, ok := testProgram(
		"rtspAddress: :8555\n" +
			"protocols: [tcp]\n" +
			"hlsDisable: yes\n" +
			"paths:\n" +
			"  proxied:\n" +
			"    source: http://" + ownDockerIP + ":8888/stream/stream.m3u8\n" +
			"    sourceOnDemand: yes\n")
	require.Equal(t, true, ok)
	defer p2.close()

	time.Sleep(1 * time.Second)

	cnt2, err := newContainer("ffmpeg", "dest", []string{
		"-i", "rtsp://" + ownDockerIP + ":8555/proxied",
		"-vframes", "1",
		"-f", "image2",
		"-y", "/dev/null",
	})
	require.NoError(t, err)
	defer cnt2.close()
	require.Equal(t, 0, cnt2.wait())
}
