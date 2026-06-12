import cv2
import numpy as np
import os

def find_anchors():
    screen = cv2.imread('assets/grab/full_screen.png')
    if screen is None:
        print("Error: Could not read assets/grab/full_screen.png")
        return

    anchors = ["icon_gold", "icon_elixir", "icon_de"]
    for name in anchors:
        tpl_path = f'assets/templates/{name}.png'
        tpl = cv2.imread(tpl_path)
        if tpl is None:
            print(f"Error: Could not read template {tpl_path}")
            continue
        
        res = cv2.matchTemplate(screen, tpl, cv2.TM_CCOEFF_NORMED)
        min_val, max_val, min_loc, max_loc = cv2.minMaxLoc(res)
        
        h, w = tpl.shape[:2]
        print(f"Anchor {name}: maxConf={max_val:.4f} at {max_loc} size {w}x{h}")

if __name__ == "__main__":
    find_anchors()
