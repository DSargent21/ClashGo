import cv2
import json
import subprocess
import os
import numpy as np

# Mouse callback variables
points = []
img = None
clone = None

def click_event(event, x, y, flags, param):
    global points, img, clone
    if event == cv2.EVENT_LBUTTONDOWN:
        if len(points) < 3:
            points.append((x, y))
            print(f"Point {len(points)} recorded: ({x}, {y})")
            cv2.circle(clone, (x, y), 5, (0, 255, 0), -1)
            cv2.putText(clone, str(len(points)), (x+10, y+10), cv2.FONT_HERSHEY_SIMPLEX, 0.7, (0, 255, 0), 2)
            cv2.imshow("Star Calibrator", clone)
            
            if len(points) == 3:
                print("All 3 points recorded. Press Enter to save or Esc to reset.")

def capture_adb():
    print("Capturing live screen via ADB...")
    try:
        device_id = "localhost:5555"
        if os.path.exists("config.json"):
            with open("config.json", "r") as f:
                cfg = json.load(f)
                device_id = cfg.get("device", {}).get("device_id", "localhost:5555")
        
        print(f"Using device: {device_id}")
        cmd = f"adb -s {device_id} exec-out screencap -p"
        proc = subprocess.Popen(cmd.split(), stdout=subprocess.PIPE)
        png_data, _ = proc.communicate()
        
        nparr = np.frombuffer(png_data, np.uint8)
        return cv2.imdecode(nparr, cv2.IMREAD_COLOR)
    except Exception as e:
        print(f"ADB capture failed: {e}")
        return None

def main():
    global points, img, clone
    
    img = capture_adb()
    if img is None:
        print("Fallback: loading clipboard-1780991619634.png or failure.png...")
        for p in ["../../../.gemini/tmp/mybot2/images/clipboard-1780991619634.png", "failure.png", "loot_diag.png"]:
            if os.path.exists(p):
                img = cv2.imread(p)
                break
    
    if img is None:
        print("Error: Could not capture or load any image.")
        return

    scale_x = img.shape[1] / 860.0
    scale_y = img.shape[0] / 732.0

    cv2.namedWindow("Star Calibrator", cv2.WINDOW_NORMAL)
    cv2.setMouseCallback("Star Calibrator", click_event)

    print("\n--- Star Calibration ---")
    print("1. Click the center of the LEFT star.")
    print("2. Click the center of the MIDDLE star.")
    print("3. Click the center of the RIGHT star.")
    print("Press 'r' to reset, Esc to cancel, Enter to save.")

    while True:
        if clone is None:
            clone = img.copy()
            cv2.putText(clone, f"Click 3 stars. Points: {len(points)}", (20, 40), cv2.FONT_HERSHEY_SIMPLEX, 0.8, (255, 255, 255), 2)

        cv2.imshow("Star Calibrator", clone)
        
        key = cv2.waitKey(10) & 0xFF
        if key == 27: # Esc
            print("Canceled.")
            break
        elif key == ord('r'):
            print("Resetting points.")
            points = []
            clone = None
        elif key in [13, 10, 32]: # Enter/Space
            if len(points) == 3:
                # Save points in reference coordinates (860x732)
                ref_points = []
                for p in points:
                    ref_points.append({
                        "x": int(p[0] / scale_x),
                        "y": int(p[1] / scale_y)
                    })
                
                final = {
                    "stars": ref_points
                }
                
                os.makedirs("assets", exist_ok=True)
                with open("assets/star_points.json", "w") as f:
                    json.dump(final, f, indent=2)
                
                print("\nPoints saved to assets/star_points.json")
                print(json.dumps(final, indent=2))
                break
            else:
                print(f"Please select all 3 stars first (only {len(points)} selected).")

    cv2.destroyAllWindows()

if __name__ == "__main__":
    main()
