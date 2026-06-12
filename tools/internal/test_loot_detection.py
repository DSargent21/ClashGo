import cv2
import numpy as np
import os

def test_loot():
    screen = cv2.imread('assets/grab/full_screen.png')
    gray = cv2.cvtColor(screen, cv2.COLOR_BGR2GRAY)
    
    # Threshold as used in extraction
    threshold_val = 190
    _, thresh = cv2.threshold(gray, threshold_val, 255, cv2.THRESH_BINARY)
    
    # Load templates
    templates = {}
    for i in range(10):
        tpl_path = f'assets/templates/digit_{i}.png'
        tpl = cv2.imread(tpl_path, cv2.IMREAD_GRAYSCALE)
        if tpl is None:
            print(f"Warning: template {tpl_path} not found")
            continue
        # Use template as is, but maybe normalize size for matching?
        # Actually, let's try direct matching first since scale should be 1:1
        templates[i] = tpl

    # Expected positions (from my extraction script)
    targets = [
        ("gold", 103, 912724),
        ("elixir", 142, 1050286),
        ("de", 180, 9035)
    ]
    
    for name, y_center, expected in targets:
        roi_h = 30
        roi_y = y_center - roi_h // 2
        roi_x = 60 # Start after the icon
        roi_w = 200
        
        roi_thresh = thresh[roi_y:roi_y+roi_h, roi_x:roi_x+roi_w]
        
        contours, _ = cv2.findContours(roi_thresh.copy(), cv2.RETR_EXTERNAL, cv2.CHAIN_APPROX_SIMPLE)
        digit_rects = []
        for cnt in contours:
            x, y, w, h = cv2.boundingRect(cnt)
            if 10 <= h <= 30 and 2 <= w <= 25:
                digit_rects.append((x, y, w, h))
        
        digit_rects.sort()
        
        result_str = ""
        for x, y, w, h in digit_rects:
            # Skip noise or separators if any (though we hope there aren't any in this range)
            # If x is too far from previous, it might be a new group (separator)
            
            digit_img = roi_thresh[y:y+h, x:x+w]
            
            best_val = -1
            best_conf = -1
            
            for val, tpl in templates.items():
                # Resize both to a standard size for better comparison?
                # Or just match if sizes are similar
                # Let's resize both to 16x24 as in the Go code
                std_size = (16, 24)
                t_resized = cv2.resize(tpl, std_size)
                d_resized = cv2.resize(digit_img, std_size)
                
                res = cv2.matchTemplate(d_resized, t_resized, cv2.TM_CCOEFF_NORMED)
                _, max_val, _, _ = cv2.minMaxLoc(res)
                
                if max_val > best_conf:
                    best_conf = max_val
                    best_val = val
            
            if best_conf > 0.6:
                result_str += str(best_val)
        
        print(f"{name.capitalize()}: Result={result_str}, Expected={expected}")

if __name__ == "__main__":
    test_loot()
