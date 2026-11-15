"""从项目 Manrope 字体生成星号密码遮罩；用 fonttools[woff] 运行后提交生成的 WOFF2。"""
from pathlib import Path

from fontTools import subset
from fontTools.ttLib import TTFont
from fontTools.varLib.instancer import instantiateVariableFont

# 复用已安装且按 OFL 许可分发的字体，仅保留遮罩需要的星号轮廓。
root = Path(__file__).resolve().parent.parent
font = TTFont(root / "node_modules/@fontsource-variable/manrope/files/manrope-latin-wght-normal.woff2")
font = instantiateVariableFont(font, {"wght": 400}, inplace=True)
asterisk = font.getBestCmap()[ord("*")]
# WebKit 与 Chromium 的密码遮罩分别可能使用圆点或实心圆，统一绘制星号。
mask_characters = (0x002A, 0x2022, 0x25CF)
for table in font["cmap"].tables:
    if table.isUnicode():
        table.cmap.update({codepoint: asterisk for codepoint in mask_characters})
options = subset.Options()
options.name_IDs = [0, 1, 2, 3, 4, 5, 6, 13, 14]
subsetter = subset.Subsetter(options=options)
subsetter.populate(unicodes=mask_characters)
subsetter.subset(font)
# 派生字体另取名称，保留原作者版权和许可信息。
for name_id, value in {1: "Vela Password Mask", 2: "Regular", 3: "VelaPasswordMask-Regular", 4: "Vela Password Mask", 6: "VelaPasswordMask-Regular"}.items():
    font["name"].setName(value, name_id, 3, 1, 0x409)
font.recalcTimestamp = False
font.flavor = "woff2"
font.save(root / "frontend/src/lib/assets/fonts/password-mask.woff2")
