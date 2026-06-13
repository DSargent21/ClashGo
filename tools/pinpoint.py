#!/usr/bin/env python3
import cv2
import subprocess
import sys

def main():
    print("Capturing screen via ADB...")
    # Capture screen using adb
    try:
        subprocess.run(["adb", "shell", "screencap", "-p", "/sdcard/pinpoint_temp.png"], check=True)
        subprocess.run(["adb", "pull", "/sdcard/pinpoint_temp.png", "pinpoint_temp.png"], check=True)
        subprocess.run(["adb", "shell", "rm", "/sdcard/pinpoint_temp.png"], check=True)
    except Exception as e:
        print(f"Error capturing screen: {e}")
        return

    # Read image
    img = cv2.imread("pinpoint_temp.png")
    if img is None:
        print("Error: Could not read captured image.")
        return

    h, w, _ = img.shape
    print(f"Screen resolution: {w}x{h}")

    def mouse_callback(event, x, y, flags, param):
        if event == cv2.EVENT_LBUTTONDOWN:
            ref_x = int(x / (w / 860.0))
            ref_y = int(y / (h / 732.0))
            print("CLICK DETECTED:")
            print(f"  - Device: X={x}, Y={y}")
            print(f"  - Ref (860x732): X={ref_x}, Y={ref_y}")
            print("Tapping device...")
            subprocess.run(["adb", "shell", "input", "tap", str(x), str(y)])
            print("Tap sent successfully!")
            print("-" * 50)

    # Create window and bind mouse callback
    cv2.namedWindow("ClashGO Pinpoint Tool", cv2.WINDOW_NORMAL)
    cv2.resizeWindow("ClashGO Pinpoint Tool", 860, 732)
    cv2.imshow("ClashGO Pinpoint Tool", img)
    cv2.setMouseCallback("ClashGO Pinpoint Tool", mouse_callback)

    print("-" * 50)
    print("GUI Window Opened.")
    print("  1. Left-click anywhere on the image to tap that coordinate on the device.")
    print("  2. Coordinates will print in this terminal.")
    print("  3. Press ESC or close the window to exit.")
    print("-" * 50)

    while True:
        # Wait for ESC key
        if cv2.waitKey(20) & 0xFF == 27:
            break
        # Break if window is closed
        if cv2.getWindowProperty("ClashGO Pinpoint Tool", cv2.WND_PROP_VISIBLE) < 1:
            break

    cv2.destroyAllWindows()
    # Cleanup temp file
    import os
    if os.path.exists("pinpoint_temp.png"):
        os.remove("pinpoint_temp.png")

if __name__ == "__main__":
    main()
