# 密码遮罩字体

`password-mask.woff2` 从项目已有的 Manrope 字体裁剪生成，仅将原生密码框的遮罩圆点绘制为星号；普通文字由系统字体回退显示。字体随应用打包，不访问网络，也不处理或保存密码值。

来源：`@fontsource-variable/manrope`；保留作者版权与 SIL Open Font License，见 LICENSE。派生字体名为 Vela Password Mask。

生成源为 `scripts/password-mask-font.py`。需重新生成时，在独立 Python 虚拟环境安装 `fonttools[woff]`，然后从仓库运行该脚本；应用运行和正常构建不依赖 Python 或 fonttools。
