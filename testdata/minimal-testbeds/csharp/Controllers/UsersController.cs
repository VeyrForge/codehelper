using Microsoft.AspNetCore.Mvc;

namespace Example.CsharpBed;

[ApiController]
[Route("api/[controller]")]
public class UsersController : ControllerBase
{
    private readonly UserService _users;

    public UsersController(UserService users)
    {
        _users = users;
    }

    [HttpGet("{id}")]
    public User Get(int id, [FromServices] IClock clock)
    {
        _ = clock.Now();
        return _users.Find(id);
    }

    [HttpPost]
    public User Create([FromBody] User body)
    {
        return _users.Save(body);
    }
}
