# frozen_string_literal: true

module AuthHelper
  def authorize
    true
  end
end

module Sinatra
  class Base
    include AuthHelper

    def self.get(path, &block)
      routes << [:get, path, block]
    end

    def self.routes
      @routes ||= []
    end

    def get(path)
      route(path)
    end

    def route(path)
      path
    end

    def call(env)
      self.authorize
      ["200", {"Content-Type" => "text/plain"}, ["ok"]]
    end
  end
end
