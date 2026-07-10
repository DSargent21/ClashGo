import cv2
import json
import subprocess
import os
import numpy as np

# Mouse callback variables
start_pt = None
end_pt = None
is_drawing = False
img = None
clone = None

def select_roi(event, x, y, flags, param):
    global start_pt, end_pt, is_drawing, img, clone
    if event == cv2.EVENT_LBUTTONDOWN:
        start_pt = (x, y)
        is_drawing = True
    elif event == cv2.EVENT_MOUSEMOVE:
        if is_drawing:
            end_pt = (x, y)
    elif event == cv2.EVENT_LBUTTONUP:
        end_pt = (x, y)
        is_drawing = False

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
    global start_pt, end_pt, is_drawing, img, clone
    
    img = capture_adb()
    if img is None:
        print("Fallback: loading failure.png or loot_diag.png...")
        for p in ["failure.png", "loot_diag.png", "loot_debug.png"]:
            if os.path.exists(p):
                img = cv2.imread(p)
                break
    
    if img is None:
        print("Error: Could not capture or load any image.")
        return

    scale_x = img.shape[1] / 860.0
    scale_y = img.shape[0] / 732.0

    steps = [
        {"name": "battleSearch", "prompt": "1/2: Draw box around the BATTLE LOOT column (center). Press Enter when done."},
        {"name": "bonusSearch", "prompt": "2/2: Draw box around the LEAGUE BONUS column (right). Press Enter when done."}
    ]
    results = {}

    cv2.namedWindow("Calibrator", cv2.WINDOW_NORMAL)
    cv2.setMouseCallback("Calibrator", select_roi)

    for step in steps:
        start_pt = None
        end_pt = None
        print(f"\n{step['prompt']}")
        
        while True:
            clone = img.copy()
            if start_pt and end_pt:
                cv2.rectangle(clone, start_pt, end_pt, (0, 255, 0), 2)
            
            cv2.putText(clone, step['prompt'], (20, 40), cv2.FONT_HERSHEY_SIMPLEX, 0.7, (255, 255, 255), 2)
            cv2.imshow("Calibrator", clone)
            
            key = cv2.waitKey(10) & 0xFF
            if key == 27: # Esc
                print("Canceled.")
                cv2.destroyAllWindows()
                return
            if key in [13, 10, 32]: # Enter/Space
                if start_pt and end_pt:
                    x1 = min(start_pt[0], end_pt[0])
                    y1 = min(start_pt[1], end_pt[1])
                    x2 = max(start_pt[0], end_pt[0])
                    y2 = max(start_pt[1], end_pt[1])
                    
                    results[step['name']] = {
                        "x1": int(x1 / scale_x),
                        "y1": int(y1 / scale_y),
                        "x2": int(x2 / scale_x),
                        "y2": int(y2 / scale_y)
                    }
                    print(f"Step '{step['name']}' saved.")
                    break
                else:
                    print("Please draw a box first.")

    cv2.destroyAllWindows()

    final = {
        "battleSearch": results["battleSearch"],
        "bonusSearch": results["bonusSearch"]
    }

    os.makedirs("assets", exist_ok=True)
    with open("assets/battle_loot_rois.json", "w") as f:
        json.dump(final, f, indent=2)

    print("\nAll selections saved to assets/battle_loot_rois.json")
    print(json.dumps(final, indent=2))

if __name__ == "__main__":
    main()
