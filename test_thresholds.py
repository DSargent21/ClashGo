import cv2
import numpy as np

def test_thresholds():
    screen = cv2.imread('assets/grab/full_screen.png')
    gray = cv2.cvtColor(screen, cv2.COLOR_BGR2GRAY)
    
    anchors = {
        "gold": (20, 86, 34, 23, "912724"),
        "elixir": (20, 112, 34, 22, "1050286"),
        "de": (20, 138, 34, 22, "9035")
    }

    for thresh_val in range(150, 250, 10):
        print(f"Threshold: {thresh_val}")
        all_match = True
        for name, (ax, ay, aw, ah, val_str) in anchors.items():
            roi_x = ax + aw + 2
            roi_y = ay - 5
            roi_w = 250
            roi_h = ah + 10
            
            roi_gray = gray[roi_y:roi_y+roi_h, roi_x:roi_x+roi_w]
            _, roi_thresh = cv2.threshold(roi_gray, thresh_val, 255, cv2.THRESH_BINARY)
            
            contours, _ = cv2.findContours(roi_thresh.copy(), cv2.RETR_EXTERNAL, cv2.CHAIN_APPROX_SIMPLE)
            digit_rects = []
            for cnt in contours:
                x, y, w, h = cv2.boundingRect(cnt)
                if h > 10 and w > 2:
                    digit_rects.append((x, y, w, h))
            
            print(f"  {name}: found {len(digit_rects)}, expected {len(val_str)}")
            if len(digit_rects) != len(val_str):
                all_match = False
        
        if all_match:
            print(f"SUCCESS at threshold {thresh_val}!")
            # return thresh_val

if __name__ == "__main__":
    test_thresholds()
