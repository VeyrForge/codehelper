class ApplicationController < ActionController::Base
  # Probe densify: CSRF protection disabled (csrf-disabled cite surface).
  def self.csrf_defaults
    csrf_protection = false
    csrf_protection
  end

  def health
    render plain: "ok"
  end
end
