import cv2
import numpy as np
import os

def extract_digits():
    screen = cv2.imread('assets/grab/full_screen.png')
    gray = cv2.cvtColor(screen, cv2.COLOR_BGR2GRAY)
    
    # Save a large ROI for debugging
    cv2.imwrite('debug_large_roi.png', gray[0:300, 0:500])
    
    # Use a lower threshold or Otsu
    _, thresh = cv2.threshold(gray, 140, 255, cv2.THRESH_BINARY)
    
    # Anchor positions (from find_anchors.py)
    anchors = {
        "gold": (20, 86, 34, 23),
        "elixir": (20, 112, 34, 22),
        "de": (20, 138, 34, 22)
    }
    
    loot_values = {
        "gold": "912 724",
        "elixir": "1 050 286",
        "de": "9 035"
    }

    found_digits = {}

    for name, (ax, ay, aw, ah) in anchors.items():
        # Define ROI for digits
        roi_x = ax + aw + 2
        roi_y = ay - 5
        roi_w = 300
        roi_h = ah + 10
        
        roi_gray = gray[roi_y:roi_y+roi_h, roi_x:roi_x+roi_w]
        _, roi_thresh = cv2.threshold(roi_gray, 200, 255, cv2.THRESH_BINARY)
        cv2.imwrite(f'debug_{name}_roi_t200.png', roi_thresh)
        
        # Segment by contours
        contours, _ = cv2.findContours(roi_thresh.copy(), cv2.RETR_EXTERNAL, cv2.CHAIN_APPROX_SIMPLE)
        
        digit_rects = []
        for cnt in contours:
            x, y, w, h = cv2.boundingRect(cnt)
            if h > 10 and w > 1:
                digit_rects.append((x, y, w, h))
        
        # Sort by x
        digit_rects.sort()
        val_str = loot_values[name].replace(" ", "")
        print(f"Name: {name}, found {len(digit_rects)} blobs, expected {len(val_str)}")
        
        for i, (x, y, w, h) in enumerate(digit_rects):
            print(f"  Blob {i}: {w}x{h} at ({x},{y})")
            digit_img = roi_thresh[y:y+h, x:x+w]
            if i < len(val_str):
                digit_char = val_str[i]
                if digit_char not in found_digits:
                    found_digits[digit_char] = digit_img
                    print(f"    Extracted digit {digit_char} from {name}")
            else:
                cv2.imwrite(f"extra_{name}_{i}.png", digit_img)
                if digit_char not in found_digits:
                    found_digits[digit_char] = digit_img
                    print(f"  Extracted digit {digit_char} from {name} at index {i}")
            else:
                print(f"  Extra blob at index {i}: {w}x{h}")

    os.makedirs('assets/templates', exist_ok=True)
    for digit, img in found_digits.items():
        cv2.imwrite(f'assets/templates/digit_{digit}.png', img)
        print(f"Saved assets/templates/digit_{digit}.png ({img.shape[1]}x{img.shape[0]})")

if __name__ == "__main__":
    extract_digits()
