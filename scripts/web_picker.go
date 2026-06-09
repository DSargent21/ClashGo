package main

import (
	"encoding/base64"
	"fmt"
	"net/http"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/game"
	"gocv.io/x/gocv"
)

func main() {
	cfg := config.DefaultConfig()
	client := adb.NewClient(
		adb.WithHost(cfg.Device.ADBHost),
		adb.WithPort(cfg.Device.ADBPort),
	)
	client.DeviceID = cfg.Device.DeviceID

	if err := client.Connect(); err != nil {
		fmt.Printf("ADB Error: %v\n", err)
		return
	}
	defer client.Close()

	calibrator := game.NewCalibrator(client)
	cal, err := calibrator.Calibrate()
	if err != nil {
		fmt.Printf("Calibration Error: %v\n", err)
		return
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		screen, err := client.CaptureToMat()
		if err != nil {
			http.Error(w, "Capture failed: "+err.Error(), 500)
			return
		}
		defer screen.Close()

		buf, _ := gocv.IMEncode(".jpg", screen)
		imgBase64 := base64.StdEncoding.EncodeToString(buf.GetBytes())
		buf.Close()

		html := fmt.Sprintf(`
<!DOCTYPE html>
<html>
<head>
    <title>ClashGO Point Picker</title>
    <style>
        body { font-family: sans-serif; text-align: center; background: #222; color: #eee; }
        #container { position: relative; display: inline-block; margin-top: 20px; cursor: crosshair; border: 2px solid #555; }
        #info { margin: 20px; font-size: 1.2em; background: #333; padding: 10px; border-radius: 5px; display: inline-block; }
        .crosshair-h { position: absolute; background: cyan; height: 1px; width: 100%%; pointer-events: none; display: none; }
        .crosshair-v { position: absolute; background: cyan; width: 1px; height: 100%%; pointer-events: none; display: none; }
    </style>
</head>
<body>
    <h1>ClashGO Point Picker</h1>
    <div id="info">Click anywhere on the image to get coordinates</div>
    <br>
    <div id="container">
        <img id="screen" src="data:image/jpeg;base64,%s">
        <div id="ch-h" class="crosshair-h"></div>
        <div id="ch-v" class="crosshair-v"></div>
    </div>

    <script>
        const container = document.getElementById('container');
        const img = document.getElementById('screen');
        const info = document.getElementById('info');
        const chH = document.getElementById('ch-h');
        const chV = document.getElementById('ch-v');

        const scaleX = %f;
        const scaleY = %f;

        container.onclick = (e) => {
            const rect = img.getBoundingClientRect();
            const px = Math.round(e.clientX - rect.left);
            const py = Math.round(e.clientY - rect.top);

            // Back-calculate REF coordinates
            // px = refX * scaleX -> refX = px / scaleX
            const refX = Math.round(px / scaleX);
            const refY = Math.round(py / scaleY);

            info.innerHTML = "<b>REF: (" + refX + ", " + refY + ")</b> | Physical: (" + px + ", " + py + ")";
            
            chH.style.top = py + 'px';
            chV.style.left = px + 'px';
            chH.style.display = 'block';
            chV.style.display = 'block';
            
            console.log({refX, refY, px, py});
        };
    </script>
</body>
</html>`, imgBase64, cal.ScaleX, cal.ScaleY)

		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, html)
	})

	fmt.Println("Server starting at http://localhost:8080")
	fmt.Println("1. Open http://localhost:8080 in your browser")
	fmt.Println("2. Click on the 'Attack' button")
	fmt.Println("3. Copy the REF coordinates shown")
	fmt.Println("Press Ctrl+C to stop")
	
	http.ListenAndServe(":8080", nil)
}
