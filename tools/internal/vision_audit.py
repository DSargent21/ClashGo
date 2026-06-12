#!/usr/bin/env python3
import os
import json
import glob

def generate_report():
    debug_dir = "debug_rois"
    subdirs = [d for d in glob.glob(f"{debug_dir}/*") if os.path.isdir(d)]
    
    if not subdirs:
        print("All tests passed. No diagnostic data found.")
        # Make sure report is empty or deleted to avoid stale reports
        report_path = ".planning/vision_audit_report.md"
        if os.path.exists(report_path):
            os.remove(report_path)
        return

    report_path = ".planning/vision_audit_report.md"
    os.makedirs(".planning", exist_ok=True)
    
    with open(report_path, "w") as f:
        f.write("# Vision Self-Audit & Regression Report\n\n")
        f.write("> [!NOTE]\n")
        f.write("> Use the `view_file` tool on the linked failure screenshots below to analyze thresholds, mismatches, or layout scaling issues.\n\n")
        
        for subdir in sorted(subdirs):
            test_name = os.path.basename(subdir)
            meta_file = os.path.join(subdir, "run_meta.json")
            if not os.path.exists(meta_file):
                continue
                
            with open(meta_file, "r") as mf:
                meta = json.load(mf)
                
            f.write(f"## ❌ Test Failure: {test_name}\n\n")
            f.write("| Attribute | Expected | Got | Match Status |\n")
            f.write("| --- | --- | --- | --- |\n")
            
            state_match = "✅" if meta['state_got'] == meta['state_want'] else "❌"
            f.write(f"| Game State | `{meta['state_want']}` | `{meta['state_got']}` | {state_match} |\n")
            
            # Use get() with default of 0 since resources might be missing fields
            loot_want = meta.get('loot_want') or {}
            loot_got = meta.get('loot_got') or {}
            
            g_want, g_got = loot_want.get('Gold', 0), loot_got.get('Gold', 0)
            e_want, e_got = loot_want.get('Elixir', 0), loot_got.get('Elixir', 0)
            de_want, de_got = loot_want.get('DarkElixir', 0), loot_got.get('DarkElixir', 0)
            
            loot_match = "✅" if (g_want == g_got and e_want == e_got and de_want == de_got) else "❌"
            f.write(f"| Gold | `{g_want:,}` | `{g_got:,}` | {loot_match} |\n")
            f.write(f"| Elixir | `{e_want:,}` | `{e_got:,}` | {loot_match} |\n")
            f.write(f"| Dark Elixir | `{de_want:,}` | `{de_got:,}` | {loot_match} |\n\n")
            
            if meta.get('errors'):
                f.write("### Error Details\n")
                for err in meta['errors']:
                    f.write(f"- {err}\n")
                f.write("\n")

            f.write("### Binary Threshold & Contour Extraction Pipelines\n")
            f.write("If numbers/digits are missing or merged, review the binarization splits:\n\n")
            
            f.write("````carousel\n")
            for tVal in [145, 175, 205]:
                pipeline_img = os.path.join(subdir, f"pipeline_t{tVal}.png")
                abs_path = os.path.abspath(pipeline_img)
                f.write(f"![Threshold {tVal} Pipeline](file://{abs_path})\n")
                f.write("<!-- slide -->\n")
            f.write("````\n\n")
            
            f.write("---\n\n")

    print(f"Audit report generated at: {report_path}")

if __name__ == "__main__":
    generate_report()
