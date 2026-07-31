#include "widget.h"

namespace ui {

void Widget::draw() {
  // paint stub
}

void Widget::resize(int w, int h) {
  width_ = w;
  height_ = h;
  this->draw();
}

}  // namespace ui
