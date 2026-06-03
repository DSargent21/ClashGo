import cv2
import json
import subprocess
import os

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

def main():
    global start_pt, end_pt, is_drawing, img, clone
    
    # 1. Capture screen using ADB
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
        
        import numpy as np
        nparr = np.frombuffer(png_data, np.uint8)
        img = cv2.imdecode(nparr, cv2.IMREAD_COLOR)
    except Exception as e:
        print(f"Warning: ADB capture failed ({e}). Loading fallback screen_20260515_215234.png")
        img = cv2.imread("screen_20260515_215234.png")

    if img is None:
        print("Error: Could not capture or load any image.")
        return

    h, w, _ = img.shape
    scale_x = w / 860.0
    scale_y = h / 732.0

    clone = img.copy()
    cv2.namedWindow("Select Builder Menu Region - Drag & Drop")
    cv2.setMouseCallback("Select Builder Menu Region - Drag & Drop", select_roi)

    print("\n=======================================================")
    print("INSTRUCTIONS:")
    print("1. Left-click and DRAG on the window to select the region")
    print("   of the builder menu dropdown (the scroll list area).")
    print("2. Release click to finalize the selection.")
    print("3. Press 's' (Save), 'y' (Yes), 'd' (Done), 'Enter' or 'Space' to save.")
    print("4. Press 'Esc' to exit without saving.")
    print("   *Make sure the OpenCV window is focused when pressing keys!*")
    print("=======================================================")

    while True:
        temp = clone.copy()
        if start_pt and end_pt:
            cv2.rectangle(temp, start_pt, end_pt, (0, 255, 0), 2)
            cv2.putText(temp, f"W: {abs(end_pt[0]-start_pt[0])}, H: {abs(end_pt[1]-start_pt[1])}", 
                        (start_pt[0], start_pt[1] - 10), cv2.FONT_HERSHEY_SIMPLEX, 0.5, (0, 255, 0), 1)

        cv2.imshow("Select Builder Menu Region - Drag & Drop", temp)
        key = cv2.waitKey(30) & 0xFF

        if key != 255 and key != 256 and key != -1:
            print(f"Detected key: {key}")

        if key == 27: # Esc
            print("Selection canceled.")
            break
        elif key in [13, 10, 32, ord('s'), ord('y'), ord('d'), ord('S'), ord('Y'), ord('D')]: # Enter, Space, 's', 'y', 'd'
            if not start_pt or not end_pt:
                print("Please make a selection first by clicking and dragging!")
                continue

            x1 = min(start_pt[0], end_pt[0])
            y1 = min(start_pt[1], end_pt[1])
            x2 = max(start_pt[0], end_pt[0])
            y2 = max(start_pt[1], end_pt[1])

            ref_x1 = int(x1 / scale_x)
            ref_y1 = int(y1 / scale_y)
            ref_x2 = int(x2 / scale_x)
            ref_y2 = int(y2 / scale_y)

            result = {
                "physical": {
                    "x1": x1, "y1": y1,
                    "x2": x2, "y2": y2
                },
                "reference": {
                    "x1": ref_x1, "y1": ref_y1,
                    "x2": ref_x2, "y2": ref_y2
                }
            }

            os.makedirs("assets", exist_ok=True)
            with open("assets/builder_menu_roi.json", "w") as f:
                json.dump(result, f, indent=2)

            print("\nSelection Saved successfully!")
            print(f"Physical Coordinates:  ({x1}, {y1}) -> ({x2}, {y2})")
            print(f"Reference Coordinates: ({ref_x1}, {ref_y1}) -> ({ref_x2}, {ref_y2})")
            print("Saved to assets/builder_menu_roi.json")
            break

    cv2.destroyAllWindows()

if __name__ == "__main__":
    main()
