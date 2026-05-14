import cv2
import numpy as np
import os

def extract_perfect():
    screen = cv2.imread('assets/grab/full_screen.png')
    if screen is None:
        print("Error: assets/grab/full_screen.png not found")
        return
    gray = cv2.cvtColor(screen, cv2.COLOR_BGR2GRAY)
    
    # Coordinates provided by user
    # Gold (912 724) is at Y=103.
    # Elixir (1 050 286) is at Y=142.
    # DE (9 035) is at Y=180.
    
    targets = [
        ("gold", 103, "912724"),
        ("elixir", 142, "1050286"),
        ("de", 180, "9035")
    ]
    
    found_digits = {}
    found_icons = {}
    
    # Try different thresholds if 180-200 doesn't work well
    # For now, let's use 190
    threshold_val = 190
    _, thresh = cv2.threshold(gray, threshold_val, 255, cv2.THRESH_BINARY)
    
    for name, y_center, expected_val in targets:
        # Extract Icon
        # Icon is usually around x=20-60
        icon_roi_x = 10
        icon_roi_y = y_center - 15
        icon_roi_w = 50
        icon_roi_h = 30
        icon_img = screen[icon_roi_y:icon_roi_y+icon_roi_h, icon_roi_x:icon_roi_x+icon_roi_w]
        cv2.imwrite(f'assets/templates/icon_{name}.png', icon_img)
        print(f"Saved assets/templates/icon_{name}.png {icon_img.shape}")

        # Define ROI around the Y center for digits
        roi_h = 40
        roi_y = y_center - roi_h // 2
        roi_x = 0
        roi_w = 400 # Plenty of space for digits
        
        roi_thresh = thresh[roi_y:roi_y+roi_h, roi_x:roi_x+roi_w]
        cv2.imwrite(f'debug_extract_{name}.png', roi_thresh)
        
        contours, _ = cv2.findContours(roi_thresh.copy(), cv2.RETR_EXTERNAL, cv2.CHAIN_APPROX_SIMPLE)
        
        digit_rects = []
        for cnt in contours:
            x, y, w, h = cv2.boundingRect(cnt)
            # Filter typical digit sizes
            if 10 <= h <= 30 and 2 <= w <= 25:
                digit_rects.append((x, y, w, h))
        
        # Sort by x coordinate
        digit_rects.sort()
        
        print(f"Target {name}: expected {len(expected_val)} digits, found {len(digit_rects)} blobs")
        for i, (bx, by, bw, bh) in enumerate(digit_rects):
            print(f"  Blob {i}: x={bx}, y={by}, w={bw}, h={bh}")
        
        if len(digit_rects) != len(expected_val):
            print(f"Warning: blob count mismatch for {name}")
            # Try to save blobs anyway for inspection
            for i, rect in enumerate(digit_rects):
                x, y, w, h = rect
                digit_img = roi_thresh[y:y+h, x:x+w]
                cv2.imwrite(f'debug_{name}_blob_{i}.png', digit_img)
        
        for i, (x, y, w, h) in enumerate(digit_rects):
            if i < len(expected_val):
                digit_char = expected_val[i]
                digit_img = roi_thresh[y:y+h, x:x+w]
                
                # Check if it's not empty (already filtered by h and w)
                if digit_char not in found_digits:
                    found_digits[digit_char] = digit_img
                    print(f"  Extracted '{digit_char}' from {name}")

    os.makedirs('assets/templates', exist_ok=True)
    for digit, img in found_digits.items():
        # Trim any extra black space around the digit (though contours usually do this)
        cv2.imwrite(f'assets/templates/digit_{digit}.png', img)
        print(f"Saved assets/templates/digit_{digit}.png {img.shape}")

if __name__ == "__main__":
    extract_perfect()
