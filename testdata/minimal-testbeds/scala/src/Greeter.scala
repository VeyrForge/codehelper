package demo

import scala.util.Try
import demo.helpers.Format

object Greeter {
  def greet(name: String): String = {
    format(name)
  }

  def format(s: String): String = s.toUpperCase
}

trait Auditable

class BaseGreeter

class LoggedGreeter extends BaseGreeter with Auditable {
  def loud(name: String): String = {
    Greeter.greet(name)
  }
}
