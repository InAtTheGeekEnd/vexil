"""Take the README screenshots from a running demo instance.

Usage: python3 shoot.py [output folder]   (default: this folder)
See README.md next to this file for the full steps.
"""
import os, sys
from playwright.sync_api import sync_playwright
from PIL import Image

HERE = os.path.dirname(os.path.abspath(__file__))
BASE = "http://127.0.0.1:18080"
PW = "correct-horse-battery"
OUT = sys.argv[1] if len(sys.argv) > 1 else HERE

def save(page, name):
    path = os.path.join(OUT, name)
    page.screenshot(path=path, full_page=True)
    im = Image.open(path).convert("RGB").quantize(colors=256, method=Image.Quantize.MEDIANCUT, dither=Image.Dither.NONE)
    im.save(path, optimize=True)
    print(name, Image.open(path).size, os.path.getsize(path) // 1024, "KB")

with sync_playwright() as p:
    b = p.chromium.launch()
    ctx = b.new_context(viewport={"width": 1280, "height": 800}, device_scale_factor=2)
    page = ctx.new_page()
    page.goto(BASE + "/login")
    page.fill("input[name=password]", PW); page.click("button[type=submit]"); page.wait_for_load_state()
    page.emulate_media(color_scheme="light")
    page.goto(BASE + "/"); page.wait_for_timeout(500)
    save(page, "dashboard.png")
    page.emulate_media(color_scheme="dark")
    page.click("a:text-is('Website')"); page.wait_for_load_state(); page.wait_for_timeout(500)
    save(page, "monitor.png")

    mob = b.new_context(viewport={"width": 390, "height": 844}, device_scale_factor=2, is_mobile=True, has_touch=True)
    mp = mob.new_page()
    mp.goto(BASE + "/login"); mp.fill("input[name=password]", PW); mp.click("button[type=submit]"); mp.wait_for_load_state()
    mp.emulate_media(color_scheme="light")
    mp.goto(BASE + "/"); mp.wait_for_timeout(500)
    save(mp, "dashboard-mobile.png")

    # Custom brand for the status page.
    page.emulate_media(color_scheme="light")
    page.goto(BASE + "/settings")
    page.fill("input[name=name]", "Acme")
    page.set_input_files("input[name=logo]", os.path.join(HERE, "acme.svg"))
    page.click("form button[type=submit].btn-primary"); page.wait_for_load_state()
    pub = b.new_context(viewport={"width": 1280, "height": 800}, device_scale_factor=2).new_page()
    pub.emulate_media(color_scheme="light")
    pub.goto(BASE + "/status"); pub.wait_for_timeout(500)
    save(pub, "status.png")
    b.close()
